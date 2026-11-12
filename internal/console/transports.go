package console

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"openopq/internal/api"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Transport settings affect protocol ingress, never the QQ connection or signer.
// A port of zero selects the existing shared console/API listener.
type transportConfig struct {
	Enabled bool `json:"enabled"`
	Port    int  `json:"port"`
}

type transportState struct {
	transportConfig
	Listening      bool   `json:"listening"`
	Address        string `json:"address"`
	Path           string `json:"path"`
	Connections    int    `json:"connections"`
	ActiveRequests int    `json:"active_requests"`
	TotalRequests  uint64 `json:"total_requests"`
	Error          string `json:"error"`
}

type transportSnapshot struct {
	SessionID  string                    `json:"session_id"`
	Scope      string                    `json:"scope"`
	Persistent bool                      `json:"persistent"`
	Services   map[string]transportState `json:"services"`
}

var transportNames = []string{"opq_http", "opq_ws", "onebot_http", "onebot_ws", "onebot12_http", "onebot12_ws"}

type transportMetric struct {
	active  int
	total   uint64
	sockets map[*trackedConn]struct{}
}

type protocolListener struct {
	listener net.Listener
	server   *http.Server
	err      error
}

type transportManager struct {
	op        sync.Mutex
	mu        sync.Mutex
	hub       *Hub
	slot      *slot
	config    map[string]transportConfig
	metrics   map[string]*transportMetric
	listeners map[int]*protocolListener
	identity  string
	dir       string
	closed    bool
}

func newTransportManager(h *Hub, s *slot) *transportManager {
	m := &transportManager{hub: h, slot: s, dir: h.options.TransportDir, config: map[string]transportConfig{}, metrics: map[string]*transportMetric{}, listeners: map[int]*protocolListener{}}
	for _, k := range transportNames {
		enabled := h.options.API.OPQ
		if strings.HasPrefix(k, "onebot") {
			enabled = h.options.API.OneBot
		}
		if strings.HasPrefix(k, "onebot12") {
			enabled = h.options.API.OneBot12
		}
		m.config[k] = transportConfig{Enabled: enabled}
		m.metrics[k] = &transportMetric{sockets: map[*trackedConn]struct{}{}}
	}
	return m
}

func protocolKind(path string) string {
	switch {
	case api.IsOneBot12Path(path):
		return "onebot12_http"
	case path == "/ws":
		return "opq_ws"
	case api.IsOneBotWSPath(path):
		return "onebot_ws"
	case api.IsOneBotPath(path):
		return "onebot_http"
	case strings.HasPrefix(path, "/v1/") || strings.HasPrefix(path, "/v2/"):
		return "opq_http"
	default:
		return ""
	}
}

func cloneTransportConfig(in map[string]transportConfig) map[string]transportConfig {
	out := make(map[string]transportConfig, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// initIdentity loads the stable device's settings once. The first anonymous QR
// session has no identity yet; its configured listeners migrate when it binds.
func (m *transportManager) initIdentity(guid string) error {
	if guid == "" {
		return nil
	}
	raw, err := hex.DecodeString(guid)
	if err != nil || len(raw) != 16 {
		return fmt.Errorf("invalid transport device identity")
	}
	m.op.Lock()
	defer m.op.Unlock()
	m.mu.Lock()
	old := m.identity
	config := cloneTransportConfig(m.config)
	m.mu.Unlock()
	if old == guid {
		return nil
	}
	if old != "" {
		return fmt.Errorf("transport device identity changed; create another account session")
	}
	if m.dir != "" {
		data, err := os.ReadFile(filepath.Join(m.dir, guid+".json"))
		if err == nil {
			if err := json.Unmarshal(data, &config); err != nil {
				return fmt.Errorf("invalid saved transport settings: %w", err)
			}
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	if err := m.applyLocked(config, guid); err != nil {
		return err
	}
	return nil
}

func (m *transportManager) save(config map[string]transportConfig, identity string) error {
	if m.dir == "" || identity == "" {
		return nil
	}
	if err := os.MkdirAll(m.dir, 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(m.dir, ".transport-*")
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
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), filepath.Join(m.dir, identity+".json"))
}

func (m *transportManager) apply(patch map[string]transportConfig) error {
	m.op.Lock()
	defer m.op.Unlock()
	m.mu.Lock()
	config, identity := cloneTransportConfig(m.config), m.identity
	m.mu.Unlock()
	for k, v := range patch {
		if _, ok := config[k]; !ok {
			return fmt.Errorf("unknown service %q", k)
		}
		config[k] = v
	}
	return m.applyLocked(config, identity)
}

// Bind every new port before publishing any setting. Conflicts and save errors
// leave all old listeners, configuration and connections intact.
func (m *transportManager) applyLocked(config map[string]transportConfig, identity string) error {
	if len(config) != len(transportNames) {
		return fmt.Errorf("invalid service configuration")
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return fmt.Errorf("session is closed")
	}
	oldConfig := cloneTransportConfig(m.config)
	oldListeners := make(map[int]*protocolListener, len(m.listeners))
	for p, l := range m.listeners {
		oldListeners[p] = l
	}
	m.mu.Unlock()
	wanted := map[int]bool{}
	for k, v := range config {
		if _, ok := oldConfig[k]; !ok || v.Port < 0 || v.Port > 65535 {
			return fmt.Errorf("invalid service or port; use 0 for shared port, or 1..65535")
		}
		if v.Enabled && v.Port > 0 {
			if v.Port == m.hub.options.SharedPort {
				return fmt.Errorf("port %d is the console's shared port; select port 0 instead", v.Port)
			}
			wanted[v.Port] = true
		}
	}
	created := map[int]*protocolListener{}
	rollback := func() {
		for _, l := range created {
			_ = l.listener.Close()
		}
	}
	for port := range wanted {
		m.mu.Lock()
		old, exists := oldListeners[port]
		healthy := exists && old.err == nil
		m.mu.Unlock()
		if healthy {
			continue
		}
		listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err != nil {
			rollback()
			return fmt.Errorf("port %d unavailable: %w", port, err)
		}
		p := port
		server := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { m.serveDedicated(p, w, r) })}
		created[port] = &protocolListener{listener: listener, server: server}
	}
	if err := m.save(config, identity); err != nil {
		rollback()
		return fmt.Errorf("save transport settings: %w", err)
	}
	var closing []*trackedConn
	var retired []*protocolListener
	m.mu.Lock()
	for k, cfg := range config {
		if cfg != oldConfig[k] {
			for c := range m.metrics[k].sockets {
				closing = append(closing, c)
			}
		}
	}
	for p, l := range m.listeners {
		if !wanted[p] || created[p] != nil {
			delete(m.listeners, p)
			retired = append(retired, l)
		}
	}
	for p, l := range created {
		m.listeners[p] = l
	}
	m.config = config
	m.identity = identity
	m.mu.Unlock()
	for _, c := range closing {
		_ = c.Close()
	}
	for _, l := range retired {
		_ = l.server.Close()
	}
	for port, l := range created {
		go func(p int, l *protocolListener) {
			err := l.server.Serve(l.listener)
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				m.mu.Lock()
				l.err = err
				m.mu.Unlock()
				m.hub.options.Logger.Error("protocol listener stopped", "session", m.slot.id, "port", p, "error", err)
			}
		}(port, l)
	}
	return nil
}

func (m *transportManager) snapshot(host string, account string) transportSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := transportSnapshot{SessionID: m.slot.id, Scope: "gateway", Persistent: m.dir != "" && m.identity != "", Services: map[string]transportState{}}
	for _, k := range transportNames {
		cfg, metric := m.config[k], m.metrics[k]
		v := transportState{transportConfig: cfg, Listening: cfg.Enabled && !m.closed, Connections: len(metric.sockets), ActiveRequests: metric.active, TotalRequests: metric.total}
		endpointHost := host
		prefix := "/accounts/" + m.slot.id
		if cfg.Port > 0 {
			endpointHost = net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.Port))
			prefix = ""
			l := m.listeners[cfg.Port]
			if l == nil {
				v.Listening = false
			} else if l.err != nil {
				v.Listening = false
				v.Error = l.err.Error()
			}
		}
		v.Address = "http://" + endpointHost
		opqPrefix := prefix
		if account != "" {
			// Public OPQ clients select an account using qq on the shared port.
			// Do not put an internal console session ID into their saved URL.
			opqPrefix = ""
		}
		switch k {
		case "opq_http":
			v.Path = opqPrefix + "/v1/LuaApiCaller?funcname=MagicCgiCmd&timeout=10"
			if account != "" {
				v.Path += "&qq=" + account
			}
		case "opq_ws":
			v.Path = opqPrefix + "/ws"
			if account != "" {
				v.Path += "?qq=" + account
			}
			v.Address = "ws://" + endpointHost
		case "onebot12_http":
			v.Path = prefix + "/onebot/v12/"
		case "onebot12_ws":
			v.Path = prefix + "/onebot/v12/"
			v.Address = "ws://" + endpointHost
		case "onebot_http":
			v.Path = prefix + "/onebot/v11/"
		case "onebot_ws":
			v.Path = prefix + "/onebot/v11/ws"
			v.Address = "ws://" + endpointHost
		}
		out.Services[k] = v
	}
	return out
}

func (m *transportManager) serveDedicated(port int, w http.ResponseWriter, r *http.Request) {
	ws := strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
	if r.URL.Path == "/" {
		key := "onebot12_http"
		oldkey := "onebot_http"
		if ws {
			key = "onebot12_ws"
			oldkey = "onebot_ws"
		}
		m.mu.Lock()
		modern, legacy := m.config[key], m.config[oldkey]
		m.mu.Unlock()
		if modern.Enabled && modern.Port == port && (!ws || !legacy.Enabled || legacy.Port != port) {
			r = r.Clone(r.Context())
			r.URL.Path = "/onebot/v12/"
		}
	}
	if r.URL.Path == "/" && ws {
		r = r.Clone(r.Context())
		r.URL.Path = "/onebot/v11/"
	}
	kind := requestProtocolKind(r)
	if kind == "" {
		http.NotFound(w, r)
		return
	}
	// A dedicated port is bound to one account. Selectors cannot reroute it.
	if q := r.URL.Query(); (q.Has("session") && (len(q["session"]) != 1 || q.Get("session") != m.slot.id)) || (q.Has("qq") && (len(q["qq"]) != 1 || q.Get("qq") != m.account())) {
		failure(w, 400, fmt.Errorf("selector does not match this account port"))
		return
	}
	m.serve(kind, port, w, r)
}

func (m *transportManager) account() string {
	if m.slot.server != nil {
		return m.slot.server.Snapshot().Account
	}
	return m.slot.account
}

func (m *transportManager) serve(kind string, port int, w http.ResponseWriter, r *http.Request) {
	m.serveAuthorized(kind, port, w, r, false)
}

func (m *transportManager) serveAuthorized(kind string, port int, w http.ResponseWriter, r *http.Request, admin bool) {
	ws := strings.HasSuffix(kind, "_ws")
	if !admin && !m.hub.options.Security.protocolGuard(w, r, ws) {
		return
	}
	m.mu.Lock()
	cfg := m.config[kind]
	if m.closed || !cfg.Enabled || cfg.Port != port {
		m.mu.Unlock()
		failure(w, http.StatusServiceUnavailable, fmt.Errorf("%s disabled on this port for the selected account", kind))
		return
	}
	metric := m.metrics[kind]
	metric.total++
	if !ws {
		metric.active++
	}
	m.mu.Unlock()
	if ws {
		w = &socketResponseWriter{ResponseWriter: w, manager: m, kind: kind, config: cfg, admin: admin, request: r}
	} else {
		defer func() { m.mu.Lock(); metric.active--; m.mu.Unlock() }()
	}
	m.hub.dispatch(m.slot, w, r)
}

type trackedConn struct {
	net.Conn
	once   sync.Once
	closed func()
}

func (c *trackedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.closed)
	return err
}

type socketResponseWriter struct {
	http.ResponseWriter
	manager *transportManager
	kind    string
	config  transportConfig
	admin   bool
	request *http.Request
}

func (w *socketResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *socketResponseWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
func (w *socketResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("hijacking unavailable")
	}
	conn, rw, err := h.Hijack()
	if err != nil {
		return nil, nil, err
	}
	m := w.manager
	c := &trackedConn{Conn: conn}
	c.closed = func() {
		m.mu.Lock()
		delete(m.metrics[w.kind].sockets, c)
		m.mu.Unlock()
		m.hub.options.Security.removeSocket(c)
	}
	m.mu.Lock()
	if m.closed || m.config[w.kind] != w.config {
		m.mu.Unlock()
		_ = conn.Close()
		return nil, nil, fmt.Errorf("service changed during websocket handshake")
	}
	m.metrics[w.kind].sockets[c] = struct{}{}
	m.mu.Unlock()
	if !m.hub.options.Security.addSocket(c, w.admin, w.request) {
		_ = c.Close()
		return nil, nil, fmt.Errorf("authorization changed during websocket handshake")
	}
	return c, rw, nil
}

func (m *transportManager) close() {
	m.op.Lock()
	defer m.op.Unlock()
	m.mu.Lock()
	m.closed = true
	var sockets []*trackedConn
	for _, metric := range m.metrics {
		for c := range metric.sockets {
			sockets = append(sockets, c)
		}
	}
	listeners := m.listeners
	m.listeners = map[int]*protocolListener{}
	m.mu.Unlock()
	for _, c := range sockets {
		_ = c.Close()
	}
	for _, l := range listeners {
		_ = l.server.Close()
	}
}

func (h *Hub) transportControl(s *slot, w http.ResponseWriter, r *http.Request) {
	if s.server != nil {
		if err := s.transports.initIdentity(s.server.Snapshot().GUID); err != nil {
			failure(w, 409, err)
			return
		}
	}
	if r.Method == http.MethodPost {
		var input struct {
			Services map[string]struct {
				Enabled *bool `json:"enabled"`
				Port    *int  `json:"port"`
			} `json:"services"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 8192)
		d := json.NewDecoder(r.Body)
		d.DisallowUnknownFields()
		if err := d.Decode(&input); err != nil {
			failure(w, 400, err)
			return
		}
		if err := d.Decode(&struct{}{}); err != io.EOF {
			failure(w, 400, fmt.Errorf("expected a single JSON object"))
			return
		}
		if len(input.Services) == 0 {
			failure(w, 400, fmt.Errorf("services is required"))
			return
		}
		patch := map[string]transportConfig{}
		for k, v := range input.Services {
			if v.Enabled == nil || v.Port == nil {
				failure(w, 400, fmt.Errorf("%s needs enabled and port", k))
				return
			}
			patch[k] = transportConfig{Enabled: *v.Enabled, Port: *v.Port}
		}
		if err := s.transports.apply(patch); err != nil {
			failure(w, 409, err)
			return
		}
		h.options.Logger.Info("account protocol settings applied", "session", s.id, "services", patch)
	} else if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	send(w, 200, s.transports.snapshot(r.Host, s.transports.account()))
}

func (h *Hub) diagnostics(s *slot, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	states := h.states()
	online := 0
	for _, v := range states {
		if v.Online {
			online++
		}
	}
	uptime := int64(0)
	if s.server != nil {
		uptime = s.server.Snapshot().Uptime
	} else {
		uptime = int64(time.Since(h.startedAt).Seconds())
	}
	send(w, 200, map[string]any{"captured_at": time.Now().UTC(), "go_version": runtime.Version(), "platform": runtime.GOOS, "arch": runtime.GOARCH, "goroutines": runtime.NumGoroutine(), "heap_alloc_bytes": mem.Alloc, "heap_sys_bytes": mem.HeapSys, "uptime_seconds": uptime, "session_count": len(states), "online_count": online, "session_id": s.id, "transports": s.transports.snapshot(r.Host, s.transports.account())})
}

// The protected, same-origin playground relay lets browsers test a dedicated
// port without weakening CSP or adding CORS to the public protocol endpoints.
// It goes through the same enable gate and counters as external callers.
func (h *Hub) relayProtocol(s *slot, w http.ResponseWriter, r *http.Request) {
	ws := r.URL.Path == "/console/api/socket"
	if (!ws && r.Method != http.MethodPost) || (ws && r.Method != http.MethodGet) {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	query := r.URL.Query()
	kind := query.Get("protocol")
	if kind != "opq" && kind != "onebot" && kind != "onebot12" {
		failure(w, 400, fmt.Errorf("protocol must be opq, onebot or onebot12"))
		return
	}
	action := query.Get("action")
	for _, c := range action {
		if c != '_' && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
			failure(w, 400, fmt.Errorf("invalid action"))
			return
		}
	}
	r = r.Clone(r.Context())
	if ws {
		if kind == "opq" {
			r.URL.Path = "/ws"
		} else if kind == "onebot12" {
			r.URL.Path = "/onebot/v12/"
		} else {
			r.URL.Path = "/onebot/v11/ws"
		}
		kind += "_ws"
	} else {
		if kind == "opq" {
			r.URL.Path = "/v1/LuaApiCaller"
		} else if kind == "onebot12" {
			r.URL.Path = "/onebot/v12/"
		} else {
			r.URL.Path = "/onebot/v11/" + action
		}
		kind += "_http"
	}
	// Account selection is fixed by the protected control API's session.
	r.URL.RawQuery = "funcname=MagicCgiCmd&timeout=10"
	s.transports.mu.Lock()
	port := s.transports.config[kind].Port
	s.transports.mu.Unlock()
	s.transports.serveAuthorized(kind, port, w, r, true)
}

func requestProtocolKind(r *http.Request) string {
	if api.IsOneBot12Path(r.URL.Path) && strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return "onebot12_ws"
	}
	return protocolKind(r.URL.Path)
}
