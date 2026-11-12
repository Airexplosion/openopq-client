package console

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"openopq/internal/api"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const consoleCookie = "openopq_console_session"
const consoleSessionAge = 12 * time.Hour
const consoleKDFIterations = 120000

type accessPolicy struct {
	Enabled bool   `json:"enabled"`
	Hash    string `json:"token_hash"`
}
type securityConfig struct {
	Version int          `json:"version"`
	Salt    string       `json:"console_salt"`
	KeyHash string       `json:"console_key_hash"`
	HTTP    accessPolicy `json:"http"`
	WS      accessPolicy `json:"ws"`
}
type loginAttempts struct {
	Count int
	Until time.Time
}

type socketAuthorization struct {
	Admin   bool
	Session [32]byte
}

// Security persists credential verifiers only. Browser sessions are random,
// revocable and intentionally do not survive a gateway restart.
type Security struct {
	mu       sync.Mutex
	config   securityConfig
	dir      string
	sessions map[[32]byte]time.Time
	attempts map[string]loginAttempts
	sockets  map[*trackedConn]socketAuthorization
}

func randomCredential() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func keyVerifier(key, salt string) string {
	b, err := pbkdf2.Key(sha256.New, key, []byte(salt), consoleKDFIterations, 32)
	if err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func tokenVerifier(token string) string {
	v := sha256.Sum256([]byte(token))
	return hex.EncodeToString(v[:])
}
func constantEqual(a, b string) bool {
	return len(a) == len(b) && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
func validCredential(key string) bool {
	return len(key) >= 16 && len(key) <= 256 && strings.TrimSpace(key) == key
}
func validVerifier(text string) bool { b, e := hex.DecodeString(text); return e == nil && len(b) == 32 }

// LoadSecurity enables mandatory console authentication for the production
// entrypoint. A nil Security is reserved for embedded library/tests compatibility.
func LoadSecurity(dir string) (*Security, error) {
	s := &Security{dir: dir, sessions: map[[32]byte]time.Time{}, attempts: map[string]loginAttempts{}, sockets: map[*trackedConn]socketAuthorization{}}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(dir, "security.json"))
	if os.IsNotExist(err) {
		key := randomCredential()
		s.config = securityConfig{Version: 1, Salt: randomCredential()}
		s.config.KeyHash = keyVerifier(key, s.config.Salt)
		// Never overwrite a bootstrap file: concurrent first starts must fail.
		file, e := os.OpenFile(filepath.Join(dir, "console-key.txt"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return nil, fmt.Errorf("create console bootstrap key: %w", e)
		}
		_, e = file.WriteString(key + "\n")
		if e == nil {
			e = file.Sync()
		}
		ce := file.Close()
		if e == nil {
			e = ce
		}
		if e != nil {
			return nil, e
		}
		if err = s.persist(s.config); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	} else {
		if err = json.Unmarshal(data, &s.config); err != nil {
			return nil, fmt.Errorf("invalid security configuration: %w", err)
		}
		c := s.config
		if c.Version != 1 || !validVerifier(c.Salt) || !validVerifier(c.KeyHash) || (c.HTTP.Hash != "" && !validVerifier(c.HTTP.Hash)) || (c.WS.Hash != "" && !validVerifier(c.WS.Hash)) || (c.HTTP.Enabled && c.HTTP.Hash == "") || (c.WS.Enabled && c.WS.Hash == "") {
			return nil, fmt.Errorf("invalid security configuration; refusing to reset credentials")
		}
	}
	return s, nil
}

func (s *Security) persist(config securityConfig) error {
	if s.dir == "" {
		return nil
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(s.dir, ".security-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	ce := f.Close()
	if err == nil {
		err = ce
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(s.dir, "security.json"))
}

func (s *Security) authenticated(r *http.Request) bool {
	if s == nil {
		return true
	}
	cookies := r.CookiesNamed(s.cookieName())
	if len(cookies) != 1 || len(cookies[0].Value) != 64 {
		return false
	}
	hash := sha256.Sum256([]byte(cookies[0].Value))
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.sessions[hash]
	if ok && time.Now().Before(exp) {
		return true
	}
	delete(s.sessions, hash)
	return false
}

func (s *Security) cookieName() string {
	// HTTP cookies ignore port. Distinct local gateways must not overwrite one
	// another's console sessions when they share 127.0.0.1.
	hash := sha256.Sum256([]byte(s.dir))
	return consoleCookie + "_" + hex.EncodeToString(hash[:6])
}

func strictJSON(w http.ResponseWriter, r *http.Request, out any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	if err := d.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("expected one JSON object")
	}
	return nil
}

func (s *Security) authEndpoint(w http.ResponseWriter, r *http.Request) bool {
	path := r.URL.Path
	if path != "/console/api/auth/status" && path != "/console/api/auth/login" && path != "/console/api/auth/logout" {
		return false
	}
	w.Header().Set("Cache-Control", "no-store")
	if path == "/console/api/auth/status" {
		if r.Method != http.MethodGet {
			w.WriteHeader(405)
			return true
		}
		send(w, 200, map[string]bool{"required": s != nil, "authenticated": s.authenticated(r)})
		return true
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return true
	}
	if s == nil {
		send(w, 200, map[string]bool{"authenticated": true})
		return true
	}
	if path == "/console/api/auth/logout" {
		if !s.authenticated(r) {
			failure(w, 401, fmt.Errorf("console login required"))
			return true
		}
		cookie, _ := r.Cookie(s.cookieName())
		hash := sha256.Sum256([]byte(cookie.Value))
		s.mu.Lock()
		delete(s.sessions, hash)
		var closing []*trackedConn
		for c, auth := range s.sockets {
			if auth.Admin && auth.Session == hash {
				closing = append(closing, c)
			}
		}
		s.mu.Unlock()
		for _, c := range closing {
			_ = c.Close()
		}
		http.SetCookie(w, &http.Cookie{Name: s.cookieName(), Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil, MaxAge: -1})
		send(w, 200, map[string]bool{"authenticated": false})
		return true
	}
	var input struct {
		Key string `json:"key"`
	}
	if err := strictJSON(w, r, &input); err != nil {
		failure(w, 400, err)
		return true
	}
	address, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		address = r.RemoteAddr
	}
	now := time.Now()
	s.mu.Lock()
	for ip, a := range s.attempts {
		if !now.Before(a.Until) {
			delete(s.attempts, ip)
		}
	}
	a := s.attempts[address]
	if a.Count >= 5 && now.Before(a.Until) {
		s.mu.Unlock()
		w.Header().Set("Retry-After", "60")
		failure(w, 429, fmt.Errorf("too many incorrect keys; retry after one minute"))
		return true
	}
	if len(s.attempts) >= 256 && a.Count == 0 {
		s.mu.Unlock()
		failure(w, 429, fmt.Errorf("login temporarily busy"))
		return true
	}
	valid := validCredential(input.Key) && constantEqual(keyVerifier(input.Key, s.config.Salt), s.config.KeyHash)
	if !valid {
		if !now.Before(a.Until) {
			a = loginAttempts{Until: now.Add(time.Minute)}
		}
		a.Count++
		s.attempts[address] = a
		s.mu.Unlock()
		failure(w, 401, fmt.Errorf("incorrect console key"))
		return true
	}
	delete(s.attempts, address)
	for key, exp := range s.sessions {
		if !now.Before(exp) {
			delete(s.sessions, key)
		}
	}
	if len(s.sessions) >= 128 {
		s.mu.Unlock()
		failure(w, 429, fmt.Errorf("console session limit reached"))
		return true
	}
	token := randomCredential()
	s.sessions[sha256.Sum256([]byte(token))] = now.Add(consoleSessionAge)
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: s.cookieName(), Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil, MaxAge: int(consoleSessionAge.Seconds())})
	send(w, 200, map[string]bool{"authenticated": true})
	return true
}

func (s *Security) consoleGuard(w http.ResponseWriter, r *http.Request) bool {
	if s.authenticated(r) {
		return true
	}
	failure(w, 401, fmt.Errorf("console login required"))
	return false
}

func (s *Security) snapshot() any {
	if s == nil {
		return map[string]any{"console": map[string]any{"required": false}, "http": map[string]bool{"enabled": false, "configured": false}, "ws": map[string]bool{"enabled": false, "configured": false}, "scope": "instance", "persistent": false}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked(false)
}
func (s *Security) snapshotLocked(reauth bool) any {
	return map[string]any{"console": map[string]any{"required": true, "key_configured": true, "session_hours": 12}, "http": map[string]bool{"enabled": s.config.HTTP.Enabled, "configured": s.config.HTTP.Hash != ""}, "ws": map[string]bool{"enabled": s.config.WS.Enabled, "configured": s.config.WS.Hash != ""}, "scope": "instance", "persistent": s.dir != "", "reauthenticate": reauth}
}

type policyPatch struct {
	Enabled *bool   `json:"enabled"`
	Token   *string `json:"token"`
}

func (s *Security) settings(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		send(w, 200, s.snapshot())
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	if s == nil {
		failure(w, 409, fmt.Errorf("security manager not configured"))
		return
	}
	var input struct {
		ConsoleKey *string      `json:"console_key"`
		HTTP       *policyPatch `json:"http"`
		WS         *policyPatch `json:"ws"`
	}
	if err := strictJSON(w, r, &input); err != nil {
		failure(w, 400, err)
		return
	}
	s.mu.Lock()
	next := s.config
	fail := func(message string) { s.mu.Unlock(); failure(w, 400, fmt.Errorf("%s", message)) }
	if input.ConsoleKey != nil && *input.ConsoleKey != "" {
		if !validCredential(*input.ConsoleKey) {
			fail("console key must be 16..256 characters without surrounding spaces")
			return
		}
		next.KeyHash = keyVerifier(*input.ConsoleKey, next.Salt)
	}
	for _, item := range []struct {
		name   string
		patch  *policyPatch
		policy *accessPolicy
	}{{"HTTP", input.HTTP, &next.HTTP}, {"WS", input.WS, &next.WS}} {
		if item.patch == nil {
			continue
		}
		if item.patch.Enabled != nil {
			item.policy.Enabled = *item.patch.Enabled
		}
		if item.patch.Token != nil && *item.patch.Token != "" {
			if !validCredential(*item.patch.Token) {
				fail(item.name + " token must be 16..256 characters without surrounding spaces")
				return
			}
			if constantEqual(keyVerifier(*item.patch.Token, next.Salt), next.KeyHash) {
				fail("console, HTTP and WS credentials must be different")
				return
			}
			item.policy.Hash = tokenVerifier(*item.patch.Token)
		}
		if item.policy.Enabled && item.policy.Hash == "" {
			fail(item.name + " authentication needs a token")
			return
		}
	}
	if next.HTTP.Hash != "" && next.HTTP.Hash == next.WS.Hash {
		fail("HTTP and WS tokens must be different")
		return
	}
	if input.ConsoleKey != nil && *input.ConsoleKey != "" {
		hash := tokenVerifier(*input.ConsoleKey)
		if hash == next.HTTP.Hash || hash == next.WS.Hash {
			fail("console, HTTP and WS credentials must be different")
			return
		}
	}
	if err := s.persist(next); err != nil {
		s.mu.Unlock()
		failure(w, 500, fmt.Errorf("cannot save security settings"))
		return
	}
	reauth := next.KeyHash != s.config.KeyHash
	closeWS := next.WS != s.config.WS
	s.config = next
	var closing []*trackedConn
	if reauth {
		clear(s.sessions)
	}
	for c, auth := range s.sockets {
		if (auth.Admin && reauth) || (!auth.Admin && closeWS) {
			closing = append(closing, c)
		}
	}
	out := s.snapshotLocked(reauth)
	s.mu.Unlock()
	for _, c := range closing {
		_ = c.Close()
	}
	if reauth {
		// The bootstrap plaintext is obsolete; the new key is retained by the user.
		if s.dir != "" {
			_ = os.Remove(filepath.Join(s.dir, "console-key.txt"))
		}
		http.SetCookie(w, &http.Cookie{Name: s.cookieName(), Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil, MaxAge: -1})
	}
	send(w, 200, out)
}

func bearerToken(r *http.Request, ws bool) (string, bool) {
	values := r.Header.Values("Authorization")
	// ParseQuery explicitly to reject malformed encodings and duplicate tokens.
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return "", false
	}
	q := query["access_token"]
	if len(values) > 1 || len(q) > 1 || (len(values) > 0 && len(q) > 0) || (!ws && len(q) > 0) {
		return "", false
	}
	if len(values) == 1 {
		parts := strings.Split(values[0], " ")
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
			return "", false
		}
		return parts[1], true
	}
	if ws && len(q) == 1 && q[0] != "" {
		return q[0], true
	}
	return "", false
}

func (s *Security) protocolGuard(w http.ResponseWriter, r *http.Request, ws bool) bool {
	if s == nil {
		return true
	}
	s.mu.Lock()
	policy := s.config.HTTP
	if ws {
		policy = s.config.WS
	}
	s.mu.Unlock()
	if !policy.Enabled {
		return true
	}
	onebot := api.IsOneBotPath(r.URL.Path)
	token, ok := bearerToken(r, ws || onebot)
	if api.IsOneBot12Path(r.URL.Path) {
		token, ok = bearerToken12(r)
	}
	if ok && constantEqual(tokenVerifier(token), policy.Hash) {
		return true
	}
	w.Header().Set("WWW-Authenticate", `Bearer realm="openopq"`)
	if onebot {
		send(w, 401, map[string]any{"status": "failed", "retcode": 1401, "data": nil, "wording": "unauthorized"})
		return false
	}
	code := 401
	failure(w, code, fmt.Errorf("valid %s bearer token required", map[bool]string{true: "WebSocket", false: "HTTP"}[ws]))
	return false
}

func (s *Security) addSocket(c *trackedConn, admin bool, r *http.Request) bool {
	if s == nil {
		return true
	}
	// Revalidate under the same lock used by settings, closing the handshake /
	// token rotation race before publishing a connected socket.
	s.mu.Lock()
	defer s.mu.Unlock()
	auth := socketAuthorization{Admin: admin}
	if admin {
		cookies := r.CookiesNamed(s.cookieName())
		if len(cookies) != 1 {
			return false
		}
		auth.Session = sha256.Sum256([]byte(cookies[0].Value))
		exp := s.sessions[auth.Session]
		if !time.Now().Before(exp) {
			return false
		}
	} else if s.config.WS.Enabled {
		token, ok := bearerToken(r, true)
		if api.IsOneBot12Path(r.URL.Path) {
			token, ok = bearerToken12(r)
		}
		if !ok || !constantEqual(tokenVerifier(token), s.config.WS.Hash) {
			return false
		}
	}
	s.sockets[c] = auth
	return true
}
func (s *Security) removeSocket(c *trackedConn) {
	if s != nil {
		s.mu.Lock()
		delete(s.sockets, c)
		s.mu.Unlock()
	}
}

func bearerToken12(r *http.Request) (string, bool) {
	values := r.Header.Values("Authorization")
	if len(values) > 0 {
		if len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") {
			return "", false
		}
		return strings.TrimPrefix(values[0], "Bearer "), true
	}
	q, e := url.ParseQuery(r.URL.RawQuery)
	if e != nil || len(q["access_token"]) != 1 {
		return "", false
	}
	return q["access_token"][0], true
}
