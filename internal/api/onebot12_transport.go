package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

type OneBot12Config struct {
	EventEnabled      *bool             `json:"event_enabled,omitempty"`
	EventBufferSize   int               `json:"event_buffer_size"`
	HeartbeatInterval int               `json:"heartbeat_interval_ms"`
	Webhooks          []OneBot12Webhook `json:"webhooks"`
	ReverseWS         []OneBot12Reverse `json:"reverse_ws"`
}
type OneBot12Webhook struct {
	URL     string `json:"url"`
	Token   string `json:"access_token,omitempty"`
	Timeout int    `json:"timeout_ms"`
}
type OneBot12Reverse struct {
	URL      string `json:"url"`
	Token    string `json:"access_token,omitempty"`
	Interval int    `json:"reconnect_interval_ms"`
}

func NormalizeOneBot12Config(c OneBot12Config) (OneBot12Config, error) {
	if c.EventEnabled == nil {
		v := true
		c.EventEnabled = &v
	}
	if c.EventBufferSize == 0 {
		c.EventBufferSize = 1024
	}
	if c.EventBufferSize < 1 || c.EventBufferSize > 100000 {
		return c, fmt.Errorf("v12 event_buffer_size must be 1..100000 (0 selects default 1024)")
	}
	if c.HeartbeatInterval < 0 || c.HeartbeatInterval > 3600000 || c.HeartbeatInterval > 0 && c.HeartbeatInterval < 1000 {
		return c, fmt.Errorf("invalid v12 heartbeat interval")
	}
	if len(c.Webhooks) > 8 || len(c.ReverseWS) > 8 {
		return c, fmt.Errorf("at most 8 v12 endpoints per transport")
	}
	valid := func(raw, scheme, token string) bool {
		u, e := url.Parse(raw)
		return e == nil && u.Host != "" && u.User == nil && u.Fragment == "" && strings.Contains("|"+scheme+"|", "|"+u.Scheme+"|") && len(token) <= 256 && !strings.ContainsAny(token, "\r\n")
	}
	for _, v := range c.Webhooks {
		if !valid(v.URL, "http|https", v.Token) || v.Timeout < 0 || v.Timeout > 60000 {
			return c, fmt.Errorf("invalid v12 webhook URL, token or timeout")
		}
	}
	for i := range c.ReverseWS {
		v := &c.ReverseWS[i]
		if v.Interval == 0 {
			v.Interval = 3000
		}
		if !valid(v.URL, "ws|wss", v.Token) || v.Interval < 1 || v.Interval > 60000 {
			return c, fmt.Errorf("invalid v12 reverse WebSocket configuration")
		}
	}
	return c, nil
}

type oneBot12Runtime struct {
	incoming, actions atomic.Int64
	s                 *Server
	ctx               context.Context
	cancel            context.CancelFunc
	mu                sync.Mutex
	config            OneBot12Config
	runCancel         context.CancelFunc
	revision          uint64
	subs              map[chan []byte]struct{}
	events            []json.RawMessage
	notify            chan struct{}
	queue             chan []byte
	status            []OneBotEndpointStatus
	files             fileStore12
}

func newOneBot12Runtime(s *Server) *oneBot12Runtime {
	ctx, cancel := context.WithCancel(context.Background())
	o := &oneBot12Runtime{s: s, ctx: ctx, cancel: cancel, subs: map[chan []byte]struct{}{}, notify: make(chan struct{}), queue: make(chan []byte, 256)}
	o.config, _ = NormalizeOneBot12Config(OneBot12Config{})
	if s.opts.OneBot12Peers != nil {
		go o.bridgePeers()
	} else {
		if src, ok := s.bot.(eventSource); ok {
			events, stop := src.Subscribe()
			go func() {
				defer stop()
				for {
					select {
					case <-ctx.Done():
						return
					case ev, ok := <-events:
						if !ok {
							return
						}
						o.ingest(s.ob.event(ev))
					}
				}
			}()
		} else if src, ok := s.bot.(interface {
			SubscribeOneBot() (<-chan []byte, func())
		}); ok {
			events, stop := src.SubscribeOneBot()
			go func() {
				defer stop()
				for {
					select {
					case <-ctx.Done():
						return
					case ev, ok := <-events:
						if !ok {
							return
						}
						o.ingest(s.ob.rawEvent(ev))
					}
				}
			}()
		}
	}
	return o
}
func (o *oneBot12Runtime) close() {
	o.cancel()
	o.mu.Lock()
	if o.runCancel != nil {
		o.runCancel()
	}
	o.mu.Unlock()
	o.files.close()
}
func (o *oneBot12Runtime) subscribe() (<-chan []byte, func()) {
	o.mu.Lock()
	ch := make(chan []byte, 256)
	o.subs[ch] = struct{}{}
	o.mu.Unlock()
	return ch, func() { o.mu.Lock(); delete(o.subs, ch); o.mu.Unlock() }
}
func (o *oneBot12Runtime) publish(b []byte) {
	if len(b) == 0 {
		return
	}
	var e map[string]any
	if json.Unmarshal(b, &e) != nil {
		return
	}
	o.mu.Lock()
	if e["type"] != "meta" && *o.config.EventEnabled {
		o.events = append(o.events, append(json.RawMessage(nil), b...))
		if len(o.events) > o.config.EventBufferSize {
			o.events = o.events[len(o.events)-o.config.EventBufferSize:]
		}
		close(o.notify)
		o.notify = make(chan struct{})
	}
	for ch := range o.subs {
		select {
		case ch <- b:
		default:
			close(ch)
			delete(o.subs, ch)
		}
	}
	o.mu.Unlock()
	select {
	case o.queue <- b:
	case <-o.ctx.Done():
	}
}
func (o *oneBot12Runtime) meta(detail string, extra map[string]any) []byte {
	e := map[string]any{"id": id12(), "time": float64(time.Now().UnixNano()) / 1e9, "type": "meta", "detail_type": detail, "sub_type": ""}
	for k, v := range extra {
		e[k] = v
	}
	b, _ := json.Marshal(e)
	return b
}
func (o *oneBot12Runtime) ingest(raw []byte) {
	if len(raw) == 0 {
		return
	}
	var old map[string]any
	if json.Unmarshal(raw, &old) != nil || old["post_type"] == "meta_event" {
		return
	}
	self := OB12Self{"qq", numberString12(old["self_id"])}
	e := map[string]any{"id": id12(), "time": old["time"], "type": "notice", "detail_type": "", "sub_type": "", "self": self}
	for _, key := range []string{"user_id", "group_id", "operator_id", "message_id"} {
		if v, ok := old[key]; ok {
			e[key] = numberString12(v)
		}
	}
	switch old["post_type"] {
	case "message", "message_sent":
		e["type"] = "message"
		e["detail_type"] = old["message_type"]
		if old["message_type"] == "private" && old["sub_type"] == "group" {
			if sender, ok := old["sender"].(map[string]any); ok {
				if group, ok := sender["group_id"]; ok {
					e["qq.temp_group_id"] = numberString12(group)
				}
			}
		}
		var alternate strings.Builder
		message := old["message"]
		if str, ok := message.(string); ok {
			message = cqSegments(str)
		}
		b, _ := json.Marshal(message)
		var segments []struct {
			Type string            `json:"type"`
			Data map[string]string `json:"data"`
		}
		_ = json.Unmarshal(b, &segments)
		out := []map[string]any{}
		for _, seg := range segments {
			switch seg.Type {
			case "text":
				alternate.WriteString(seg.Data["text"])
			case "at":
				if seg.Data["qq"] == "all" {
					alternate.WriteString("@全体成员")
				} else {
					alternate.WriteString("@" + seg.Data["qq"])
				}
			case "image":
				alternate.WriteString("[图片]")
			case "record":
				alternate.WriteString("[语音]")
			case "video":
				alternate.WriteString("[视频]")
			case "face":
				alternate.WriteString("[表情]")
			default:
				alternate.WriteString("[" + seg.Type + "]")
			}
			typ := seg.Type
			data := map[string]any{}
			switch typ {
			case "text":
				data["text"] = seg.Data["text"]
			case "at":
				typ = "mention"
				data["user_id"] = seg.Data["qq"]
				if seg.Data["qq"] == "all" {
					typ = "mention_all"
					data = map[string]any{}
				}
			case "image", "record", "video":
				kind := typ
				if typ == "record" {
					typ = "voice"
				}
				file := seg.Data["file"]
				if file == "" {
					file = seg.Data["url"]
				}
				data["file_id"] = o.files.remote(kind, file, seg.Data["url"])
			default:
				typ = "qq." + typ
				for k, v := range seg.Data {
					data[k] = v
				}
			}
			out = append(out, map[string]any{"type": typ, "data": data})
		}
		e["message"] = out
		e["alt_message"] = alternate.String()
	case "request":
		e["type"] = "qq.request"
		e["detail_type"] = old["request_type"]
		e["sub_type"] = old["sub_type"]
		if e["sub_type"] == nil {
			e["sub_type"] = ""
		}
		e["qq.flag"], e["qq.comment"] = old["flag"], old["comment"]
	case "notice":
		switch old["notice_type"] {
		case "group_increase":
			e["detail_type"], e["sub_type"] = "group_member_increase", "join"
		case "group_decrease":
			e["detail_type"], e["sub_type"] = "group_member_decrease", old["sub_type"]
			if old["sub_type"] == "kick_me" {
				e["sub_type"] = "kick"
			}
		case "group_recall":
			e["detail_type"], e["sub_type"] = "group_message_delete", "recall"
			if e["operator_id"] != e["user_id"] {
				e["sub_type"] = "delete"
			}
		case "friend_recall":
			e["detail_type"], e["sub_type"] = "private_message_delete", "recall"
		default:
			e["detail_type"] = "qq." + fmt.Sprint(old["notice_type"])
			for k, v := range old {
				if k != "post_type" && k != "self_id" && k != "time" {
					if _, ok := e[k]; !ok {
						e["qq."+k] = v
					}
				}
			}
		}
	default:
		return
	}
	b, _ := json.Marshal(e)
	o.publish(b)
}
func (o *oneBot12Runtime) visible(ctx context.Context, b []byte) bool {
	if o.s.opts.OneBot12Peers == nil {
		return true
	}
	var e struct {
		Self *OB12Self `json:"self"`
	}
	_ = json.Unmarshal(b, &e)
	if e.Self == nil {
		return true
	}
	_, err := o.s.select12(ctx, e.Self)
	return err == nil
}
func (o *oneBot12Runtime) latest(ctx context.Context, a ob12Action) ob12Response {
	var p struct {
		Limit   int64 `json:"limit"`
		Timeout int64 `json:"timeout"`
	}
	if json.Unmarshal(a.Params, &p) != nil || p.Limit < 0 || p.Timeout < 0 || p.Timeout > 60 {
		return fail12(bad12(10003, "limit must be >=0 and timeout 0..60 seconds"), a.Echo)
	}
	o.mu.Lock()
	enabled := *o.config.EventEnabled
	o.mu.Unlock()
	if !enabled {
		return fail12(bad12(10002, "HTTP event polling is disabled"), a.Echo)
	}
	wait, cancel := context.WithTimeout(ctx, time.Duration(p.Timeout)*time.Second)
	defer cancel()
	for {
		o.mu.Lock()
		var batch []json.RawMessage
		if len(o.events) > 0 {
			n := len(o.events)
			if p.Limit > 0 && int64(n) > p.Limit {
				n = int(p.Limit)
			}
			batch = append([]json.RawMessage{}, o.events[:n]...)
			o.events = o.events[n:]
		}
		notify := o.notify
		o.mu.Unlock()
		out := []json.RawMessage{}
		for _, b := range batch {
			if o.visible(ctx, b) {
				out = append(out, b)
			}
		}
		if len(out) > 0 || p.Timeout == 0 {
			return ok12(out, a.Echo)
		}
		select {
		case <-wait.Done():
			return ok12(out, a.Echo)
		case <-o.ctx.Done():
			return fail12(bad12(35000, "adapter closed"), a.Echo)
		case <-notify:
		}
	}
}
func (o *oneBot12Runtime) bridgePeers() {
	type sub struct{ stop func() }
	known := map[*Server]sub{}
	defer func() {
		for _, v := range known {
			v.stop()
		}
	}()
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		wanted := map[*Server]bool{}
		ctx := context.WithValue(o.ctx, ob12TransportKey{}, "any")
		for _, p := range o.s.peers12(ctx) {
			if p.v12 == nil {
				continue
			}
			wanted[p] = true
			if _, ok := known[p]; ok {
				continue
			}
			events, stop := p.v12.subscribe()
			child, cancel := context.WithCancel(o.ctx)
			known[p] = sub{func() { cancel(); stop() }}
			go func() {
				for {
					select {
					case <-child.Done():
						return
					case b, ok := <-events:
						if !ok {
							return
						}
						var v map[string]any
						_ = json.Unmarshal(b, &v)
						if v["type"] != "meta" {
							o.publish(b)
						}
					}
				}
			}()
		}
		for p, v := range known {
			if !wanted[p] {
				v.stop()
				delete(known, p)
			}
		}
		select {
		case <-o.ctx.Done():
			return
		case <-tick.C:
		}
	}
}
func (o *oneBot12Runtime) configure(c OneBot12Config) {
	o.mu.Lock()
	if o.runCancel != nil {
		o.runCancel()
	}
	ctx, cancel := context.WithCancel(o.ctx)
	o.runCancel = cancel
	o.config = c
	o.revision++
	rev := o.revision
	o.status = nil
	for i := range c.Webhooks {
		o.status = append(o.status, OneBotEndpointStatus{Kind: "webhook", Index: i})
	}
	for i := range c.ReverseWS {
		o.status = append(o.status, OneBotEndpointStatus{Kind: "reverse_ws", Index: i})
	}
	o.mu.Unlock()
	go o.deliver(ctx, rev, c)
	go func() {
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		last := ""
		var heartbeat time.Time
		for {
			status := o.s.status12(context.WithValue(ctx, ob12TransportKey{}, "any"))
			b, _ := json.Marshal(status)
			if string(b) != last {
				o.publish(o.meta("status_update", map[string]any{"status": status}))
				last = string(b)
			}
			if c.HeartbeatInterval > 0 && time.Since(heartbeat) >= time.Duration(c.HeartbeatInterval)*time.Millisecond {
				o.publish(o.meta("heartbeat", map[string]any{"interval": c.HeartbeatInterval}))
				heartbeat = time.Now()
			}
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
		}
	}()
	for i, p := range c.ReverseWS {
		go o.reverse(ctx, rev, i, p)
	}
}
func (o *oneBot12Runtime) endpoint(rev uint64, kind string, index int, ok, connected bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if rev != o.revision {
		return
	}
	for i := range o.status {
		v := &o.status[i]
		if v.Kind == kind && v.Index == index {
			v.Connected = connected
			if ok {
				v.Sent++
				v.Error = ""
			} else {
				v.Failures++
				v.Error = "connection or request failed"
			}
		}
	}
}
func (o *oneBot12Runtime) deliver(ctx context.Context, rev uint64, c OneBot12Config) {
	for {
		select {
		case <-ctx.Done():
			return
		case b := <-o.queue:
			for i, p := range c.Webhooks {
				o.post(ctx, rev, i, p, b)
			}
		}
	}
}
func (o *oneBot12Runtime) post(ctx context.Context, rev uint64, index int, p OneBot12Webhook, b []byte) {
	actionContext := ctx
	if p.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(p.Timeout)*time.Millisecond)
		defer cancel()
	}
	req, e := http.NewRequestWithContext(ctx, "POST", p.URL, bytes.NewReader(b))
	if e != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "OneBot/12 (qq) OpenOPQ/"+version12()["version"].(string))
	req.Header.Set("X-OneBot-Version", "12")
	req.Header.Set("X-Impl", "openopq")
	if p.Token != "" {
		req.Header.Set("Authorization", "Bearer "+p.Token)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, e := client.Do(req)
	if e != nil {
		o.endpoint(rev, "webhook", index, false, false)
		return
	}
	defer res.Body.Close()
	ok := res.StatusCode == 200 || res.StatusCode == 204
	o.endpoint(rev, "webhook", index, ok, ok)
	if res.StatusCode != 200 {
		return
	}
	ct, _, _ := mime.ParseMediaType(res.Header.Get("Content-Type"))
	if ct != "application/json" {
		o.s.logger.Warn("OneBot v12 webhook returned unsupported content type", "index", index)
		return
	}
	body, e := io.ReadAll(io.LimitReader(res.Body, (8<<20)+1))
	var actions []json.RawMessage
	if e != nil || len(body) > 8<<20 || json.Unmarshal(body, &actions) != nil {
		o.s.logger.Warn("invalid OneBot v12 webhook actions", "index", index)
		return
	}
	for _, a := range actions {
		reply := o.s.execute12(actionContext, a)
		if reply.Status != "ok" {
			o.s.logger.Warn("OneBot v12 webhook action failed", "retcode", reply.Retcode)
		}
	}
}
func (o *oneBot12Runtime) serveWS(w http.ResponseWriter, r *http.Request) {
	o.incoming.Add(1)
	defer o.incoming.Add(-1)
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	c, e := up.Upgrade(w, r, nil)
	if e != nil {
		return
	}
	defer c.Close()
	o.socket(r.Context(), c)
}
func (o *oneBot12Runtime) socket(ctx context.Context, c *websocket.Conn) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	events, stop := o.subscribe()
	defer stop()
	responses := make(chan ob12Response, 64)
	if c.WriteMessage(websocket.TextMessage, o.meta("connect", map[string]any{"version": version12()})) != nil {
		return
	}
	if c.WriteMessage(websocket.TextMessage, o.meta("status_update", map[string]any{"status": o.s.status12(ctx)})) != nil {
		return
	}
	go func() {
		defer cancel()
		c.SetReadLimit(45 << 20)
		for {
			typ, b, e := c.ReadMessage()
			if e != nil {
				return
			}
			reply := fail12(bad12(10001, "MessagePack binary frames are not supported"), "")
			if typ == websocket.TextMessage {
				reply = o.s.execute12(ctx, b)
			}
			select {
			case responses <- reply:
			case <-ctx.Done():
				return
			}
		}
	}()
	ping := time.NewTicker(30 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-o.ctx.Done():
			return
		case b, ok := <-events:
			if !ok {
				return
			}
			if o.visible(ctx, b) {
				_ = c.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if c.WriteMessage(websocket.TextMessage, b) != nil {
					return
				}
			}
		case reply := <-responses:
			_ = c.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if c.WriteJSON(reply) != nil {
				return
			}
		case <-ping.C:
			if c.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)) != nil {
				return
			}
		}
	}
}
func (o *oneBot12Runtime) reverse(ctx context.Context, rev uint64, index int, p OneBot12Reverse) {
	for ctx.Err() == nil {
		headers := http.Header{}
		headers.Set("User-Agent", "OneBot/12 (qq) OpenOPQ")
		if p.Token != "" {
			headers.Set("Authorization", "Bearer "+p.Token)
		}
		d := websocket.Dialer{HandshakeTimeout: 10 * time.Second, Subprotocols: []string{"12.openopq"}}
		c, res, e := d.DialContext(ctx, p.URL, headers)
		if res != nil && res.Body != nil {
			_ = res.Body.Close()
		}
		if e == nil {
			o.endpoint(rev, "reverse_ws", index, true, true)
			done := make(chan struct{})
			go func() {
				select {
				case <-ctx.Done():
					c.Close()
				case <-done:
				}
			}()
			o.socket(context.WithValue(ctx, ob12TransportKey{}, "ws"), c)
			close(done)
			c.Close()
		}
		o.endpoint(rev, "reverse_ws", index, false, false)
		timer := time.NewTimer(time.Duration(p.Interval) * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
func (s *Server) OneBot12Snapshot() (OneBot12Config, []OneBotEndpointStatus) {
	if s.v12 == nil {
		return OneBot12Config{}, nil
	}
	s.v12.mu.Lock()
	defer s.v12.mu.Unlock()
	b, _ := json.Marshal(s.v12.config)
	var c OneBot12Config
	_ = json.Unmarshal(b, &c)
	return c, append([]OneBotEndpointStatus{}, s.v12.status...)
}

// Keep decimal QQ identifiers stable when decoding JSON through generic maps.
func decimal12(v any) string {
	if n, ok := v.(float64); ok {
		return strconv.FormatFloat(n, 'f', -1, 64)
	}
	return fmt.Sprint(v)
}

func (s *Server) OneBot12Metrics() map[string]int64 {
	if s.v12 == nil {
		return map[string]int64{}
	}
	s.v12.mu.Lock()
	events := len(s.v12.events)
	s.v12.mu.Unlock()
	return map[string]int64{"connections": s.v12.incoming.Load(), "actions": s.v12.actions.Load(), "pending_events": int64(events)}
}
