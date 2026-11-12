package console

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"openopq/internal/api"
	"openopq/internal/device"
)

type slot struct {
	id         string
	server     *Server
	remote     *url.URL
	proxy      *httputil.ReverseProxy
	account    string
	compat     http.Handler
	transports *transportManager
	onebot     *oneBotSettings
}
type SessionState struct {
	ID       string `json:"id"`
	External bool   `json:"external"`
	State
}

// Hub owns independent consoles and connections. Selecting a tab never closes one.
type Hub struct {
	mu        sync.Mutex
	options   Options
	slots     []*slot
	claims    map[string]string
	closed    bool
	startedAt time.Time
	v12       *api.Server
}

func NewHub(opt Options) *Hub {
	h := &Hub{options: opt, claims: map[string]string{}, startedAt: time.Now()}
	_, _ = h.Add("")
	h.v12 = api.NewOneBot12Gateway(h.oneBot12Peers, opt.Logger, opt.API.OneBotConfig.V12)
	return h
}
func (h *Hub) defaultSlot() *slot {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.slots) == 0 {
		return nil
	}
	return h.slots[0]
}
func (h *Hub) find(id string) *slot {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, s := range h.slots {
		if s.id == id {
			return s
		}
	}
	return nil
}
func (h *Hub) Add(account string) (string, error) {
	if account != "" {
		var err error
		account, err = device.Account(account)
		if err != nil {
			return "", err
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return "", fmt.Errorf("console is closed")
	}
	if len(h.slots) >= 8 {
		return "", fmt.Errorf("session limit reached")
	}
	for _, s := range h.slots {
		if account != "" && (s.account == account || h.claims[account] == s.id) {
			return s.id, nil
		}
	}
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	id := hex.EncodeToString(raw)
	opt := h.options
	opt.API.ClusterInfo = h.clusterInfo
	// Managed ingress controls adapter availability independently per transport.
	// Keep both implementations ready so enabling OneBot never needs a relogin.
	opt.API.OPQ, opt.API.OneBot, opt.API.OneBot12 = true, true, true
	opt.API.OneBot12UserID = account
	level := slog.LevelInfo
	if h.options.Logs != nil {
		level = h.options.Logs.level
	}
	logs := NewLogBuffer(level)
	logger := slog.New(MultiHandler(logs, h.options.Logger.Handler())).With("session", id)
	opt.Logs, opt.Logger = logs, logger
	opt.OnStopped = func() { h.release(id) }
	if opt.QRFile != "" {
		opt.QRFile = filepath.Join(filepath.Dir(opt.QRFile), "login-qr-"+id+".png")
	}
	opt.Factory = func(guid []byte, uin uint32) (ManagedBot, error) {
		var b ManagedBot
		var err error
		if h.options.SessionFactory != nil {
			b, err = h.options.SessionFactory(guid, uin, logger)
		} else {
			b, err = h.options.Factory(guid, uin)
		}
		if err != nil {
			return nil, err
		}
		if guard, ok := b.(interface{ SetAccountGuard(func(uint32) error) }); ok {
			guard.SetAccountGuard(func(u uint32) error { return h.claim(id, fmt.Sprint(u)) })
		}
		return b, nil
	}
	s := &slot{id: id, server: New(opt), account: account}
	if account != "" {
		if err := s.server.Select(account); err != nil {
			_ = s.server.Close()
			return "", err
		}
	}
	s.transports = newTransportManager(h, s)
	s.onebot = newOneBotSettings(h, s)
	if err := s.transports.initIdentity(s.server.Snapshot().GUID); err != nil {
		_ = s.server.Close()
		return "", err
	}
	if err := s.onebot.init(s.server.Snapshot().GUID); err != nil {
		s.transports.close()
		_ = s.server.Close()
		return "", err
	}
	h.slots = append(h.slots, s)
	return id, nil
}
func (h *Hub) claim(id, account string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if owner := h.claims[account]; owner != "" && owner != id {
		return fmt.Errorf("this QQ is already connected in another session")
	}
	for a, owner := range h.claims {
		if owner == id && a != account {
			delete(h.claims, a)
		}
	}
	h.claims[account] = id
	return nil
}
func (h *Hub) release(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for a, owner := range h.claims {
		if owner == id {
			delete(h.claims, a)
		}
	}
}
func (h *Hub) Select(account string) error {
	s := h.defaultSlot()
	if s == nil {
		return fmt.Errorf("no session")
	}
	if err := s.server.Select(account); err != nil {
		return err
	}
	h.mu.Lock()
	s.account = s.server.Snapshot().Account
	h.mu.Unlock()
	return nil
}
func (h *Hub) Begin(account string) error {
	s := h.defaultSlot()
	if s == nil {
		return fmt.Errorf("no session")
	}
	return s.server.Begin(account)
}
func (h *Hub) BeginAccount(account string) error {
	id, err := h.Add(account)
	if err != nil {
		return err
	}
	s := h.find(id)
	if s.server == nil {
		return fmt.Errorf("account belongs to the attached console")
	}
	if err := h.claim(id, account); err != nil {
		return err
	}
	if err := s.server.Begin(account); err != nil {
		h.release(id)
		return err
	}
	return nil
}

// Attach keeps an already-running local login alive during a console upgrade.
// Only explicitly configured loopback HTTP consoles are accepted.
func (h *Hub) Attach(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil {
		return err
	}
	validPath := u.Path == ""
	if strings.HasPrefix(u.Path, "/accounts/") {
		id := strings.TrimPrefix(u.Path, "/accounts/")
		raw, decodeErr := hex.DecodeString(id)
		validPath = decodeErr == nil && len(raw) == 12 && id == strings.ToLower(id) && u.RawPath == ""
	}
	if u.Scheme != "http" || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" && u.Hostname() != "::1") || u.User != nil || !validPath || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("attach-console must be a loopback HTTP origin or exact /accounts/<session> URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", u.String()+"/console/api/status", nil)
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer r.Body.Close()
	var state State
	if r.StatusCode != 200 || json.NewDecoder(r.Body).Decode(&state) != nil {
		return fmt.Errorf("cannot read attached console")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return fmt.Errorf("console is closed")
	}
	for _, existing := range h.slots {
		if existing.remote != nil && existing.remote.String() == u.String() {
			return nil
		}
		if state.Account != "" && (existing.account == state.Account || h.claims[state.Account] == existing.id) {
			return fmt.Errorf("this account is already managed by the console")
		}
	}
	if len(h.slots) >= 8 {
		return fmt.Errorf("session limit reached")
	}
	p := httputil.NewSingleHostReverseProxy(u)
	director := p.Director
	p.Director = func(r *http.Request) { director(r); r.Host = u.Host; r.Header.Del("Origin") }
	id := "attached"
	used := make(map[string]struct{}, len(h.slots))
	for _, existing := range h.slots {
		used[existing.id] = struct{}{}
	}
	if _, exists := used[id]; exists {
		for suffix := 1; ; suffix++ {
			candidate := fmt.Sprintf("attached-%d", suffix)
			if _, exists := used[candidate]; !exists {
				id = candidate
				break
			}
		}
	}
	s := &slot{id: id, remote: u, proxy: p, account: state.Account}
	compatOptions := h.options.API
	compatOptions.ClusterInfo = h.clusterInfo
	compatOptions.OPQ, compatOptions.OneBot, compatOptions.OneBot12 = true, true, true
	compatOptions.OneBot12UserID = state.Account
	s.compat = api.NewServer(newAttachedBot(u), h.options.Logger, compatOptions)
	s.transports = newTransportManager(h, s)
	s.onebot = newOneBotSettings(h, s)
	if err := s.transports.initIdentity(state.GUID); err != nil {
		return err
	}
	h.slots = append(h.slots, s)
	if err := s.onebot.init(state.GUID); err != nil {
		return err
	}
	if state.Online {
		h.claims[state.Account] = s.id
	}
	return nil
}
func (h *Hub) states() []SessionState {
	h.mu.Lock()
	slots := append([]*slot(nil), h.slots...)
	h.mu.Unlock()
	out := make([]SessionState, 0, len(slots))
	for _, s := range slots {
		var v State
		if s.server != nil {
			v = s.server.Snapshot()
		} else {
			client := &http.Client{Timeout: 2 * time.Second}
			r, e := client.Get(s.remote.String() + "/console/api/status")
			if e == nil {
				if r.StatusCode == 200 {
					e = json.NewDecoder(r.Body).Decode(&v)
				} else {
					e = fmt.Errorf("attached status returned HTTP %d", r.StatusCode)
				}
				r.Body.Close()
			}
			if e != nil {
				v = State{Phase: "offline", Account: s.account, Error: "attached instance unavailable"}
			}
		}
		if v.Account == "" && v.Phase == "idle" && len(slots) > 1 && s == slots[0] {
			continue
		}
		v.QR = ""
		out = append(out, SessionState{ID: s.id, External: s.remote != nil, State: v})
	}
	return out
}
func (h *Hub) Close() error {
	h.mu.Lock()
	h.closed = true
	slots := append([]*slot(nil), h.slots...)
	h.mu.Unlock()
	var first error
	if h.v12 != nil {
		_ = h.v12.Close()
	}
	for _, s := range slots {
		s.transports.close()
		if adapter, ok := s.compat.(*api.Server); ok {
			_ = adapter.Close()
		}
		if s.server != nil {
			if err := s.server.Close(); err != nil && first == nil {
				first = err
			}
		}
	}
	return first
}
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" && strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		r = r.Clone(r.Context())
		r.URL.Path = "/onebot/v11/"
	}
	// Explicit per-session protocol URLs work for HTTP and WebSocket clients.
	id := r.URL.Query().Get("session")
	if strings.HasPrefix(r.URL.Path, "/accounts/") {
		parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/accounts/"), "/", 2)
		if len(parts) != 2 {
			http.NotFound(w, r)
			return
		}
		id = parts[0]
		r = r.Clone(r.Context())
		r.URL.Path = "/" + parts[1]
	}
	if !protectConsole(w, r) {
		return
	}
	path := r.URL.Path
	if h.options.Security.authEndpoint(w, r) {
		return
	}
	if strings.HasPrefix(path, "/console/api/") {
		if !h.options.Security.consoleGuard(w, r) {
			return
		}
		if path == "/console/api/security" {
			h.options.Security.settings(w, r)
			return
		}
	} else if protocolKind(path) != "" {
		if !h.options.Security.protocolGuard(w, r, strings.HasSuffix(requestProtocolKind(r), "_ws")) {
			return
		}
	}
	if api.IsOneBot12Path(path) && id == "" {
		h.serveOneBot12(w, r)
		return
	}
	if h.routeOneBot(w, r, &id) {
		return
	}
	if h.routeOPQ(w, r, &id) {
		return
	}
	if strings.HasPrefix(path, "/console/api/sessions/") && r.Method == "DELETE" {
		id := strings.TrimPrefix(path, "/console/api/sessions/")
		s := h.find(id)
		if s == nil || s.server == nil {
			failure(w, 409, fmt.Errorf("attached or unknown session cannot be removed"))
			return
		}
		if s == h.defaultSlot() {
			failure(w, 409, fmt.Errorf("default session is retained for legacy interfaces"))
			return
		}
		phase := s.server.Snapshot().Phase
		if phase == "online" || phase == "reconnecting" || phase == "restoring" || phase == "requesting" || phase == "waiting" || phase == "authenticating" || phase == "securing" || phase == "registering" {
			failure(w, 409, fmt.Errorf("disconnect or cancel this session first"))
			return
		}
		if err := s.server.Disconnect(); err != nil {
			failure(w, 409, err)
			return
		}
		if err := s.server.Close(); err != nil {
			failure(w, 500, err)
			return
		}
		s.transports.close()
		h.mu.Lock()
		for i, v := range h.slots {
			if v.id == id {
				h.slots = append(h.slots[:i], h.slots[i+1:]...)
				break
			}
		}
		h.mu.Unlock()
		h.release(id)
		send(w, 200, map[string]bool{"ok": true})
		return
	}
	if path == "/console/api/sessions" && r.Method == "GET" {
		send(w, 200, h.states())
		return
	}
	if strings.HasPrefix(path, "/console/") && !strings.HasPrefix(path, "/console/api/") || path == "/" {
		h.defaultSlot().server.ServeHTTP(w, r)
		return
	}
	if path == "/console/api/sessions" && r.Method == "POST" {
		var v struct {
			Account string `json:"account"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
			failure(w, 400, err)
			return
		}
		id, err := h.Add(v.Account)
		if err != nil {
			failure(w, 409, err)
			return
		}
		send(w, 201, map[string]string{"id": id})
		return
	}
	var s *slot
	if id != "" {
		s = h.find(id)
	} else {
		s = h.defaultSlot()
	}
	if s == nil {
		failure(w, 404, fmt.Errorf("unknown session"))
		return
	}
	if path == "/console/api/onebot12" {
		s.onebot.control12(w, r)
		return
	}
	if path == "/console/api/onebot" {
		s.onebot.control(w, r)
		return
	}
	if path == "/console/api/transports" {
		h.transportControl(s, w, r)
		return
	}
	if path == "/console/api/diagnostics" {
		h.diagnostics(s, w, r)
		return
	}
	if path == "/console/api/relay" || path == "/console/api/socket" {
		h.relayProtocol(s, w, r)
		return
	}
	if (path == "/console/api/login" || path == "/console/api/resume") && r.Method == "POST" {
		var v struct {
			Account string `json:"account"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
			failure(w, 400, err)
			return
		}
		if s.server == nil {
			failure(w, 409, fmt.Errorf("create a new session to log in"))
			return
		}
		// A configured account tab keeps its device when its optional input is
		// left blank. Anonymous new-device login belongs in a newly added tab.
		if v.Account == "" {
			v.Account = s.account
			if v.Account == "" {
				v.Account = s.server.Snapshot().Account
			}
		}
		if s.server.Snapshot().Online {
			failure(w, 409, fmt.Errorf("session is online; add another account"))
			return
		}
		current := s.server.Snapshot().Phase
		if current == "restoring" || current == "reconnecting" || current == "requesting" || current == "waiting" || current == "authenticating" || current == "securing" || current == "registering" {
			failure(w, 409, fmt.Errorf("this session is already logging in"))
			return
		}
		if v.Account != "" {
			a, err := device.Account(v.Account)
			if err != nil {
				failure(w, 400, err)
				return
			}
			if err = h.claim(s.id, a); err != nil {
				failure(w, 409, err)
				return
			}
		}
		start := s.server.Begin
		if path == "/console/api/resume" {
			start = s.server.Resume
		}
		if err := start(v.Account); err != nil {
			phase := s.server.Snapshot().Phase
			if phase != "restoring" && phase != "requesting" && phase != "waiting" && phase != "authenticating" && phase != "securing" && phase != "registering" {
				h.release(s.id)
			}
			failure(w, 409, err)
			return
		}
		send(w, 202, map[string]bool{"accepted": true})
		return
	}
	if path == "/console/api/disconnect" && r.Method == "POST" {
		if s.server != nil {
			if err := s.server.Disconnect(); err != nil {
				failure(w, 409, err)
				return
			}
			h.release(s.id)
			send(w, 200, map[string]bool{"ok": true})
			return
		}
		h.release(s.id)
	}
	if kind := requestProtocolKind(r); kind != "" {
		s.transports.serve(kind, 0, w, r)
		return
	}
	h.dispatch(s, w, r)
}

func (h *Hub) dispatch(s *slot, w http.ResponseWriter, r *http.Request) {
	if s.server == nil {
		// Gateway credentials are consumed here; never leak them to an attached
		// older console, its logs, or its WebSocket backend.
		r = r.Clone(r.Context())
		r.Header.Del("Cookie")
		r.Header.Del("Authorization")
		r.Header.Del("X-Console-Key")
		query := r.URL.Query()
		query.Del("access_token")
		r.URL.RawQuery = query.Encode()
	}
	if s.server == nil && s.compat != nil && (r.URL.Path == "/v1/LuaApiCaller" || (api.IsOneBotPath(r.URL.Path) || api.IsOneBot12Path(r.URL.Path))) {
		s.compat.ServeHTTP(w, r)
	} else if s.server == nil {
		s.proxy.ServeHTTP(w, r)
	} else {
		s.server.ServeHTTP(w, r)
	}
}
