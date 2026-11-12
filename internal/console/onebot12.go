package console

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"

	"openopq/internal/api"
)

func (h *Hub) oneBot12Peers(ctx context.Context) []*api.Server {
	h.mu.Lock()
	slots := append([]*slot{}, h.slots...)
	closed := h.closed
	h.mu.Unlock()
	if closed {
		return nil
	}
	kind := api.OneBot12Transport(ctx)
	out := []*api.Server{}
	for _, s := range slots {
		if s.server != nil && s.server.Snapshot().Account == "" {
			continue
		}
		if s.server == nil && s.account == "" {
			continue
		}
		if s.transports == nil {
			continue
		}
		s.transports.mu.Lock()
		a, b := s.transports.config["onebot12_http"], s.transports.config["onebot12_ws"]
		enabled := a.Enabled && a.Port == 0
		if kind == "ws" {
			enabled = b.Enabled && b.Port == 0
		} else if kind == "any" {
			enabled = enabled || (b.Enabled && b.Port == 0)
		}
		s.transports.mu.Unlock()
		if !enabled {
			continue
		}
		var adapter *api.Server
		if s.server != nil {
			s.server.mu.RLock()
			adapter, _ = s.server.api.(*api.Server)
			s.server.mu.RUnlock()
		} else {
			adapter, _ = s.compat.(*api.Server)
		}
		if adapter != nil {
			out = append(out, adapter)
		}
	}
	return out
}
func (h *Hub) serveOneBot12(w http.ResponseWriter, r *http.Request) {
	if h.v12 == nil {
		http.NotFound(w, r)
		return
	}
	kind := "http"
	ws := false
	if requestProtocolKind(r) == "onebot12_ws" {
		kind = "ws"
		ws = true
	}
	ctx := api.OneBot12WithTransport(r.Context(), kind)
	h.mu.Lock()
	slots := append([]*slot{}, h.slots...)
	h.mu.Unlock()
	enabled := false
	for _, s := range slots {
		if s.transports == nil {
			continue
		}
		key := "onebot12_http"
		if ws {
			key = "onebot12_ws"
		}
		s.transports.mu.Lock()
		cfg := s.transports.config[key]
		s.transports.mu.Unlock()
		enabled = enabled || (cfg.Enabled && cfg.Port == 0)
	}
	if !enabled {
		failure(w, 503, fmt.Errorf("OneBot v12 is disabled on this shared transport"))
		return
	}
	if ws {
		w = &oneBot12SocketWriter{ResponseWriter: w, security: h.options.Security, request: r}
	}
	h.v12.ServeHTTP(w, r.WithContext(ctx))
}

type oneBot12SocketWriter struct {
	http.ResponseWriter
	security *Security
	request  *http.Request
}

func (w *oneBot12SocketWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("hijacking unavailable")
	}
	conn, buf, e := h.Hijack()
	if e != nil {
		return nil, nil, e
	}
	var tracked *trackedConn
	tracked = &trackedConn{Conn: conn, closed: func() { w.security.removeSocket(tracked) }}
	if !w.security.addSocket(tracked, false, w.request) {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("WebSocket token changed during handshake")
	}
	return tracked, buf, nil
}
func (m *oneBotSettings) adapter12() *api.Server {
	if m.slot.server != nil {
		m.slot.server.mu.RLock()
		defer m.slot.server.mu.RUnlock()
		a, _ := m.slot.server.api.(*api.Server)
		return a
	}
	a, _ := m.slot.compat.(*api.Server)
	return a
}
func (m *oneBotSettings) control12(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.slot.server != nil {
		if e := m.initLocked(m.slot.server.Snapshot().GUID); e != nil {
			failure(w, 409, e)
			return
		}
	}
	if r.Method == "POST" {
		var input api.OneBot12Config
		if e := strictJSON(w, r, &input); e != nil {
			failure(w, 400, e)
			return
		}
		for i := range input.Webhooks {
			v := &input.Webhooks[i]
			if v.Token == "" && i < len(m.config.V12.Webhooks) && v.URL == m.config.V12.Webhooks[i].URL {
				v.Token = m.config.V12.Webhooks[i].Token
			}
		}
		for i := range input.ReverseWS {
			v := &input.ReverseWS[i]
			if v.Token == "" && i < len(m.config.V12.ReverseWS) && v.URL == m.config.V12.ReverseWS[i].URL {
				v.Token = m.config.V12.ReverseWS[i].Token
			}
		}
		c, e := api.NormalizeOneBot12Config(input)
		if e != nil {
			failure(w, 400, e)
			return
		}
		next := m.config
		next.V12 = c
		if e = m.save(next); e != nil {
			failure(w, 500, e)
			return
		}
		if e = m.apply(next); e != nil {
			failure(w, 500, e)
			return
		}
		m.config = next
	} else if r.Method != "GET" {
		w.WriteHeader(405)
		return
	}
	adapter := m.adapter12()
	if adapter == nil {
		failure(w, 503, fmt.Errorf("account adapter unavailable"))
		return
	}
	c, status := adapter.OneBot12Snapshot()
	webhooks := []map[string]any{}
	reverse := []map[string]any{}
	for _, v := range c.Webhooks {
		webhooks = append(webhooks, map[string]any{"url": v.URL, "timeout_ms": v.Timeout, "token_configured": v.Token != ""})
	}
	for _, v := range c.ReverseWS {
		reverse = append(reverse, map[string]any{"url": v.URL, "reconnect_interval_ms": v.Interval, "token_configured": v.Token != ""})
	}
	result := map[string]any{"session_id": m.slot.id, "persistent": m.guid != "" && m.directory() != "", "config": map[string]any{"event_enabled": c.EventEnabled, "event_buffer_size": c.EventBufferSize, "heartbeat_interval_ms": c.HeartbeatInterval, "webhooks": webhooks, "reverse_ws": reverse}, "endpoints": status, "account_metrics": adapter.OneBot12Metrics()}
	if m.h.v12 != nil {
		result["shared_metrics"] = m.h.v12.OneBot12Metrics()
	}
	send(w, 200, result)
}
