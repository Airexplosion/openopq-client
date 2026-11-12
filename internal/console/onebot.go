package console

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"openopq/internal/api"
)

type oneBotSettings struct {
	mu     sync.Mutex
	h      *Hub
	slot   *slot
	guid   string
	config api.OneBotConfig
}

func newOneBotSettings(h *Hub, s *slot) *oneBotSettings {
	c, _ := api.NormalizeOneBotConfig(h.options.API.OneBotConfig)
	return &oneBotSettings{h: h, slot: s, config: c}
}
func (m *oneBotSettings) directory() string {
	if m.h.options.TransportDir == "" {
		return ""
	}
	return filepath.Join(m.h.options.TransportDir, "onebot")
}
func (m *oneBotSettings) crypt() (cipher.AEAD, error) {
	dir := m.directory()
	if e := os.MkdirAll(dir, 0700); e != nil {
		return nil, e
	}
	path := filepath.Join(dir, "state.key")
	key, e := os.ReadFile(path)
	if os.IsNotExist(e) {
		key = make([]byte, 32)
		if _, e = rand.Read(key); e != nil {
			return nil, e
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if os.IsExist(err) {
			key, e = os.ReadFile(path)
		} else if err != nil {
			return nil, err
		} else {
			_, e = f.Write(key)
			if e == nil {
				e = f.Sync()
			}
			ce := f.Close()
			if e == nil {
				e = ce
			}
		}
	}
	if e != nil {
		return nil, e
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("invalid OneBot state key")
	}
	block, e := aes.NewCipher(key)
	if e != nil {
		return nil, e
	}
	return cipher.NewGCM(block)
}
func (m *oneBotSettings) save(c api.OneBotConfig) error {
	if m.directory() == "" || m.guid == "" {
		return nil
	}
	a, e := m.crypt()
	if e != nil {
		return e
	}
	nonce := make([]byte, a.NonceSize())
	if _, e = rand.Read(nonce); e != nil {
		return e
	}
	raw, e := json.Marshal(c)
	if e != nil {
		return e
	}
	data := a.Seal(nonce, nonce, raw, []byte(m.guid))
	f, e := os.CreateTemp(m.directory(), ".onebot-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(data)
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	return os.Rename(f.Name(), filepath.Join(m.directory(), m.guid+".enc"))
}
func (m *oneBotSettings) apply(c api.OneBotConfig) error {
	if m.slot.server != nil {
		s := m.slot.server
		s.mu.Lock()
		defer s.mu.Unlock()
		s.opt.API.OneBotConfig = c
		return s.api.(*api.Server).ConfigureOneBot(c)
	}
	return m.slot.compat.(*api.Server).ConfigureOneBot(c)
}
func (m *oneBotSettings) init(guid string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.initLocked(guid)
}
func (m *oneBotSettings) initLocked(guid string) error {
	if guid == "" || m.guid == guid {
		return nil
	}
	raw, e := hex.DecodeString(guid)
	if e != nil || len(raw) != 16 {
		return fmt.Errorf("invalid OneBot account identity")
	}
	if m.guid != "" {
		return fmt.Errorf("OneBot account identity changed")
	}
	c := m.config
	if dir := m.directory(); dir != "" {
		data, err := os.ReadFile(filepath.Join(dir, guid+".enc"))
		if err == nil {
			a, e := m.crypt()
			if e != nil {
				return e
			}
			if len(data) < a.NonceSize() {
				return fmt.Errorf("invalid encrypted OneBot config")
			}
			raw, e := a.Open(nil, data[:a.NonceSize()], data[a.NonceSize():], []byte(guid))
			if e != nil {
				return fmt.Errorf("OneBot config authentication failed")
			}
			if e = json.Unmarshal(raw, &c); e != nil {
				return e
			}
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	c, e = api.NormalizeOneBotConfig(c)
	if e != nil {
		return e
	}
	m.guid = guid
	if e = m.save(c); e != nil {
		m.guid = ""
		return e
	}
	m.config = c
	return m.apply(c)
}
func (m *oneBotSettings) control(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.slot.server != nil {
		if e := m.initLocked(m.slot.server.Snapshot().GUID); e != nil {
			failure(w, 409, e)
			return
		}
	}
	if r.Method == http.MethodPost {
		var input api.OneBotConfig
		r.Body = http.MaxBytesReader(w, r.Body, 32768)
		d := json.NewDecoder(r.Body)
		d.DisallowUnknownFields()
		if e := d.Decode(&input); e != nil {
			failure(w, 400, e)
			return
		}
		if e := d.Decode(&struct{}{}); e != io.EOF {
			failure(w, 400, fmt.Errorf("expected one JSON object"))
			return
		}
		// Empty secret preserves the previous secret only for the same indexed URL.
		for i := range input.HTTPPost {
			p := &input.HTTPPost[i]
			if p.Secret == "" && i < len(m.config.HTTPPost) && p.URL == m.config.HTTPPost[i].URL {
				p.Secret = m.config.HTTPPost[i].Secret
			}
		}
		for i := range input.ReverseWS {
			p := &input.ReverseWS[i]
			if p.Token == "" && i < len(m.config.ReverseWS) && p.URL == m.config.ReverseWS[i].URL {
				p.Token = m.config.ReverseWS[i].Token
			}
		}
		input.V12 = m.config.V12
		c, e := api.NormalizeOneBotConfig(input)
		if e != nil {
			failure(w, 400, e)
			return
		}
		if e = m.save(c); e != nil {
			failure(w, 500, e)
			return
		}
		if e = m.apply(c); e != nil {
			failure(w, 500, e)
			return
		}
		m.config = c
		m.h.options.Logger.Info("OneBot account connections configured", "session", m.slot.id, "http_post_count", len(c.HTTPPost), "reverse_ws_count", len(c.ReverseWS))
	} else if r.Method != http.MethodGet {
		w.WriteHeader(405)
		return
	}
	var adapter *api.Server
	if m.slot.server != nil {
		m.slot.server.mu.RLock()
		adapter, _ = m.slot.server.api.(*api.Server)
		m.slot.server.mu.RUnlock()
	} else {
		adapter, _ = m.slot.compat.(*api.Server)
	}
	c, status := adapter.OneBotSnapshot()
	type post struct {
		URL        string `json:"url"`
		Timeout    int    `json:"timeout_ms"`
		Configured bool   `json:"secret_configured"`
	}
	type reverse struct {
		URL        string `json:"url"`
		Role       string `json:"role"`
		Interval   int    `json:"reconnect_interval_ms"`
		Configured bool   `json:"token_configured"`
	}
	posts := []post{}
	reverses := []reverse{}
	for _, p := range c.HTTPPost {
		posts = append(posts, post{p.URL, p.Timeout, p.Secret != ""})
	}
	for _, p := range c.ReverseWS {
		reverses = append(reverses, reverse{p.URL, p.Role, p.ReconnectInterval, p.Token != ""})
	}
	send(w, 200, map[string]any{"session_id": m.slot.id, "persistent": m.guid != "" && m.directory() != "", "config": map[string]any{"message_format": c.MessageFormat, "heartbeat_interval_ms": c.HeartbeatInterval, "rate_limit_interval_ms": c.RateLimitInterval, "http_post": posts, "reverse_ws": reverses}, "endpoints": status})
}
