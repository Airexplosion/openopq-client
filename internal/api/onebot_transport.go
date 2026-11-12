package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"openopq/internal/protocol"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// OneBotConfig is per account. Outbound credentials are write-only in the console.
type OneBotConfig struct {
	V12               OneBot12Config  `json:"v12"`
	MessageFormat     string          `json:"message_format"`
	HeartbeatInterval int             `json:"heartbeat_interval_ms"`
	RateLimitInterval int             `json:"rate_limit_interval_ms"`
	HTTPPost          []OneBotPost    `json:"http_post"`
	ReverseWS         []OneBotReverse `json:"reverse_ws"`
}
type OneBotPost struct {
	URL     string `json:"url"`
	Secret  string `json:"secret,omitempty"`
	Timeout int    `json:"timeout_ms"`
}
type OneBotReverse struct {
	URL               string `json:"url"`
	Role              string `json:"role"`
	Token             string `json:"token,omitempty"`
	ReconnectInterval int    `json:"reconnect_interval_ms"`
}

func NormalizeOneBotConfig(c OneBotConfig) (OneBotConfig, error) {
	if c.MessageFormat == "" {
		c.MessageFormat = "array"
	}
	if c.MessageFormat != "array" && c.MessageFormat != "string" {
		return c, fmt.Errorf("message_format must be array or string")
	}
	if c.RateLimitInterval == 0 {
		c.RateLimitInterval = 500
	}
	if c.HeartbeatInterval < 0 || (c.HeartbeatInterval > 0 && c.HeartbeatInterval < 1000) || c.HeartbeatInterval > 3600000 {
		return c, fmt.Errorf("heartbeat interval must be 0 or 1000..3600000 ms")
	}
	if c.RateLimitInterval < 1 || c.RateLimitInterval > 60000 {
		return c, fmt.Errorf("rate interval must be 1..60000 ms")
	}
	if len(c.HTTPPost) > 8 || len(c.ReverseWS) > 8 {
		return c, fmt.Errorf("at most 8 endpoints per transport")
	}
	validURL := func(value string, schemes string) bool {
		u, e := url.Parse(value)
		return e == nil && u.Host != "" && u.User == nil && u.Fragment == "" && strings.Contains("|"+schemes+"|", "|"+u.Scheme+"|")
	}
	for i := range c.HTTPPost {
		p := &c.HTTPPost[i]
		if !validURL(p.URL, "http|https") {
			return c, fmt.Errorf("invalid HTTP POST URL")
		}
		if p.Timeout == 0 {
			p.Timeout = 5000
		}
		if p.Timeout < 100 || p.Timeout > 60000 || len(p.Secret) > 256 {
			return c, fmt.Errorf("invalid HTTP POST timeout or secret")
		}
	}
	for i := range c.ReverseWS {
		p := &c.ReverseWS[i]
		if !validURL(p.URL, "ws|wss") {
			return c, fmt.Errorf("invalid reverse WebSocket URL")
		}
		if p.Role == "" {
			p.Role = "Universal"
		}
		if p.Role != "API" && p.Role != "Event" && p.Role != "Universal" {
			return c, fmt.Errorf("reverse role must be API, Event or Universal")
		}
		if p.ReconnectInterval == 0 {
			p.ReconnectInterval = 3000
		}
		if p.ReconnectInterval < 500 || p.ReconnectInterval > 60000 || len(p.Token) > 256 || strings.ContainsAny(p.Token, "\r\n") {
			return c, fmt.Errorf("invalid reverse reconnect interval or token")
		}
	}
	var e error
	c.V12, e = NormalizeOneBot12Config(c.V12)
	if e != nil {
		return c, e
	}
	return c, nil
}

var oneBotActions = strings.Fields(`send_private_msg send_group_msg send_msg delete_msg get_msg get_forward_msg get_group_system_msg send_like set_group_kick set_group_ban set_group_anonymous_ban set_group_whole_ban set_group_admin set_group_anonymous set_group_card set_group_name set_group_leave set_group_special_title set_friend_add_request set_group_add_request get_login_info get_stranger_info get_friend_list get_group_info get_group_list get_group_member_info get_group_member_list get_group_honor_info get_cookies get_csrf_token get_credentials get_record get_image can_send_image can_send_record get_status get_version_info set_restart clean_cache .handle_quick_operation group_poke send_group_poke send_poke friend_poke get_supported_actions get_onebot_capabilities`)

func actionBase(a string) string {
	for _, suffix := range []string{"_async", "_rate_limited"} {
		a = strings.TrimSuffix(a, suffix)
	}
	return a
}
func knownAction(a string) bool {
	a = actionBase(a)
	for _, n := range oneBotActions {
		if n == a {
			return true
		}
	}
	return false
}
func oneBotPathAction(path string) string {
	return strings.Trim(strings.TrimPrefix(path, "/onebot/v11"), "/")
}
func IsOneBotWSPath(path string) bool {
	p := strings.TrimSuffix(path, "/")
	return p == "/onebot/v11" || p == "/onebot/v11/ws" || p == "/onebot/v11/api" || p == "/onebot/v11/event" || p == "/api" || p == "/event"
}
func IsOneBotPath(path string) bool {
	return strings.HasPrefix(path, "/onebot/v11/") || path == "/onebot/v11" || IsOneBotWSPath(path) || knownAction(strings.Trim(path, "/"))
}

// Root / is reserved for the management page on the shared port; dedicated
// OneBot ports rewrite it to /onebot/v11/ before reaching this adapter.

func normalizeOBParams(raw json.RawMessage, action string) (json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return json.RawMessage(`{}`), nil
	}
	var p map[string]json.RawMessage
	if err := json.Unmarshal(raw, &p); err != nil || p == nil {
		return nil, fmt.Errorf("params must be an object")
	}
	numbers := strings.Fields("user_id group_id message_id duration times delay")
	bools := strings.Fields("auto_escape enable reject_add_request is_dismiss approve no_cache at_sender auto_escape anonymous")
	for _, k := range append(numbers, bools...) {
		// Forward resources are opaque strings; message_id is an alias for id
		// on this action, not necessarily a numeric cached message ID.
		if action == "get_forward_msg" && k == "message_id" {
			continue
		}
		v, ok := p[k]
		if !ok {
			continue
		}
		var text string
		if json.Unmarshal(v, &text) != nil {
			continue
		}
		isBool := false
		for _, b := range bools {
			if k == b {
				isBool = true
			}
		}
		if isBool {
			b, err := strconv.ParseBool(text)
			if err != nil {
				return nil, fmt.Errorf("invalid boolean %s", k)
			}
			p[k] = json.RawMessage(strconv.FormatBool(b))
		} else {
			n, err := strconv.ParseInt(text, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid integer %s", k)
			}
			p[k] = json.RawMessage(strconv.FormatInt(n, 10))
		}
	}
	return json.Marshal(p)
}

func (s *Server) serveOneBotHTTP(w http.ResponseWriter, r *http.Request) {
	fail := func(status int, wording string, echo json.RawMessage) {
		response := obBad(wording, echo)
		if status == http.StatusRequestEntityTooLarge {
			response.Retcode = 1413
		}
		writeJSON(w, status, response)
	}
	if r.Method != "GET" && r.Method != "POST" {
		fail(405, "method not allowed", nil)
		return
	}
	action := oneBotPathAction(r.URL.Path)
	params := map[string]json.RawMessage{}
	// HTTP query/form values may themselves be JSON (message arrays, echo,
	// booleans). Keep their JSON types; otherwise treat them as literal text.
	add := func(values url.Values) error {
		for k, vs := range values {
			if k == "access_token" || k == "qq" || k == "session" {
				continue
			}
			if len(vs) != 1 {
				return fmt.Errorf("duplicate parameter %s", k)
			}
			value := json.RawMessage(vs[0])
			if !json.Valid(value) {
				value, _ = json.Marshal(vs[0])
			}
			params[k] = value
		}
		return nil
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		fail(400, "bad request: malformed query", nil)
		return
	}
	if err = add(query); err != nil {
		fail(400, err.Error(), nil)
		return
	}
	if r.Method == "GET" && action == "" {
		status := s.ob.statusData(r.Context())
		writeJSON(w, 200, obOK(map[string]any{"online": status["online"]}, nil))
		return
	}
	if r.Method == "POST" {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<20))
		if err != nil {
			fail(413, "body too large", nil)
			return
		}
		if len(bytes.TrimSpace(body)) > 0 {
			ct, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil && r.Header.Get("Content-Type") != "" {
				fail(400, "bad request: unsupported content-type", nil)
				return
			}
			switch ct {
			case "application/json", "":
				var object map[string]json.RawMessage
				if err := json.Unmarshal(body, &object); err != nil || object == nil {
					if ct == "application/json" {
						fail(400, "bad request: invalid json", nil)
						return
					}
					form, e := url.ParseQuery(string(body))
					if e != nil {
						fail(400, "bad request: malformed form", nil)
						return
					}
					if e = add(form); e != nil {
						fail(400, e.Error(), nil)
						return
					}
				} else {
					for k, v := range object {
						params[k] = v
					}
				}
			case "application/x-www-form-urlencoded":
				form, err := url.ParseQuery(string(body))
				if err != nil {
					fail(400, "bad request: malformed form", nil)
					return
				}
				if err = add(form); err != nil {
					fail(400, err.Error(), nil)
					return
				}
			default:
				fail(400, "bad request: unsupported content-type: "+ct, nil)
				return
			}
		}
	}
	echo := params["echo"]
	if action == "" {
		if value, ok := params["action"]; ok {
			if json.Unmarshal(value, &action) != nil {
				fail(400, "bad request: action must be a string", echo)
				return
			}
		}
	}
	delete(params, "action")
	delete(params, "echo")
	if action == "" {
		fail(400, "bad request: missing action", echo)
		return
	}
	var raw json.RawMessage
	if nested, ok := params["params"]; ok {
		raw = nested
	} else {
		raw, _ = json.Marshal(params)
	}
	ctx, cancel := context.WithTimeout(r.Context(), defaultCallTimeout)
	defer cancel()
	// Once an action is parsed, business and validation errors use the same
	// HTTP 200 JSON envelope as WebSocket responses. Ingress errors stay 4xx.
	writeJSON(w, 200, s.handleOneBotAction(ctx, obAction{Action: action, Params: raw, Echo: echo}))
}

type oneBotRuntime struct {
	startedAt   time.Time
	eventQueue  chan []byte
	runContext  context.Context
	s           *Server
	ctx         context.Context
	cancel      context.CancelFunc
	mu          sync.Mutex
	config      OneBotConfig
	runCancel   context.CancelFunc
	subscribers map[chan []byte]struct{}
	cache       map[int32]obCachedMessage
	keys        map[string]int32
	order       []int32
	next        int32
	queue       chan obAction
	queueOnce   sync.Once
	status      []OneBotEndpointStatus
	revision    uint64
}
type OneBotEndpointStatus struct {
	Kind      string `json:"kind"`
	Index     int    `json:"index"`
	Connected bool   `json:"connected"`
	Sent      uint64 `json:"sent"`
	Failures  uint64 `json:"failures"`
	Error     string `json:"error,omitempty"`
}

func newOneBotRuntime(s *Server) *oneBotRuntime {
	ctx, cancel := context.WithCancel(context.Background())
	o := &oneBotRuntime{s: s, ctx: ctx, cancel: cancel, startedAt: time.Now(), subscribers: map[chan []byte]struct{}{}, cache: map[int32]obCachedMessage{}, keys: map[string]int32{}, next: int32(time.Now().UnixNano() & 0x3fffffff), queue: make(chan obAction, 64), eventQueue: make(chan []byte, 128)}
	go o.deliverEvents()
	o.config, _ = NormalizeOneBotConfig(OneBotConfig{})
	if s.opts.OneBot {
		if src, ok := s.bot.(interface {
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
						o.queueEvent(o.rawEvent(ev))
					}
				}
			}()
		}
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
						o.queueEvent(o.event(ev))
					}
				}
			}()
		}
	}
	return o
}

func (o *oneBotRuntime) rawEvent(b []byte) []byte {
	var v map[string]any
	if json.Unmarshal(b, &v) != nil {
		return nil
	}
	if v["post_type"] == "meta_event" {
		return nil
	}
	if v["post_type"] == "message" || v["post_type"] == "message_sent" {
		num := func(k string) uint32 { n, _ := v[k].(float64); return uint32(n) }
		group, user, seq := num("group_id"), num("user_id"), num("message_id")
		cq, _ := v["raw_message"].(string)
		message := v["message"]
		if o.format() == "array" {
			if _, ok := message.(string); ok {
				message = cqSegments(cq)
			}
		} else {
			message = cq
		}
		v["message"] = message
		target := protocol.SendTarget{Group: group}
		if group == 0 {
			target.Uin = user
		}
		data := cachedOneBotEvent(v)
		id := o.remember(obCachedMessage{Key: fmt.Sprintf("remote:%d:%d:%d:%d", group, user, seq, num("time")), Target: target, Result: protocol.SendResult{Sequence: seq, Timestamp: num("time")}, Data: data})
		v["message_id"] = id
	}
	out, _ := json.Marshal(v)
	return out
}
func (o *oneBotRuntime) close() {
	o.cancel()
	o.mu.Lock()
	if o.runCancel != nil {
		o.runCancel()
	}
	o.mu.Unlock()
}
func (o *oneBotRuntime) subscribe() (<-chan []byte, func()) {
	o.mu.Lock()
	ch := make(chan []byte, 128)
	o.subscribers[ch] = struct{}{}
	o.mu.Unlock()
	return ch, func() { o.mu.Lock(); delete(o.subscribers, ch); o.mu.Unlock() }
}
func (o *oneBotRuntime) publish(b []byte) {
	if len(b) == 0 {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	for ch := range o.subscribers {
		select {
		case ch <- b:
		default: // Disconnect a slow consumer; never silently lose an API response.
			close(ch)
			delete(o.subscribers, ch)
		}
	}
}
func (o *oneBotRuntime) format() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.config.MessageFormat
}
func (s *Server) ConfigureOneBot(c OneBotConfig) error {
	c, e := NormalizeOneBotConfig(c)
	if e != nil {
		return e
	}
	old, _ := s.OneBotSnapshot()
	before, after := old, c
	before.V12, after.V12 = OneBot12Config{}, OneBot12Config{}
	s.ob.mu.Lock()
	started := s.ob.runContext != nil
	s.ob.mu.Unlock()
	if !started || !reflect.DeepEqual(before, after) {
		s.ob.configure(c)
	} else {
		s.ob.mu.Lock()
		s.ob.config.V12 = c.V12
		s.ob.mu.Unlock()
	}
	if s.v12 != nil {
		previous, _ := s.OneBot12Snapshot()
		s.v12.mu.Lock()
		active := s.v12.runCancel != nil
		s.v12.mu.Unlock()
		if !active || !reflect.DeepEqual(previous, c.V12) {
			s.v12.configure(c.V12)
		}
	}
	return nil
}
func (s *Server) OneBotSnapshot() (OneBotConfig, []OneBotEndpointStatus) {
	s.ob.mu.Lock()
	defer s.ob.mu.Unlock()
	data, _ := json.Marshal(s.ob.config)
	var c OneBotConfig
	_ = json.Unmarshal(data, &c)
	return c, append([]OneBotEndpointStatus{}, s.ob.status...)
}
func (o *oneBotRuntime) configure(c OneBotConfig) {
	o.mu.Lock()
	if o.runCancel != nil {
		o.runCancel()
	}
	ctx, cancel := context.WithCancel(o.ctx)
	o.runCancel = cancel
	o.runContext = ctx
	o.config = c
	o.revision++
	rev := o.revision
	o.status = nil
	for i := range c.HTTPPost {
		o.status = append(o.status, OneBotEndpointStatus{Kind: "http_post", Index: i})
	}
	for i := range c.ReverseWS {
		o.status = append(o.status, OneBotEndpointStatus{Kind: "reverse_ws", Index: i})
	}
	o.mu.Unlock()
	if c.HeartbeatInterval > 0 {
		go func() {
			t := time.NewTicker(time.Duration(c.HeartbeatInterval) * time.Millisecond)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					o.queueEvent(o.meta("heartbeat", "", c.HeartbeatInterval))
				}
			}
		}()
	}
	for i, p := range c.HTTPPost {
		go func(i int, p OneBotPost) {
			for ctx.Err() == nil {
				self, err := o.s.bot.Self(ctx)
				if err == nil && self.Uin != 0 && self.Online {
					o.post(ctx, rev, i, p, o.meta("lifecycle", "enable", 0))
					return
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Second):
				}
			}
		}(i, p)
	}

	for i, p := range c.ReverseWS {
		go o.reverse(ctx, rev, i, p)
	}
}
func (o *oneBotRuntime) meta(kind, sub string, interval int) []byte {
	self, _ := o.s.bot.Self(o.ctx)
	v := map[string]any{"time": time.Now().Unix(), "self_id": self.Uin, "post_type": "meta_event", "meta_event_type": kind}
	if sub != "" {
		v["sub_type"] = sub
	}
	if interval > 0 {
		v["interval"] = interval
		v["status"] = o.statusData(o.ctx)
	}
	b, _ := json.Marshal(v)
	return b
}
func (o *oneBotRuntime) update(rev uint64, kind string, index int, connected, success bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if rev != o.revision {
		return
	}
	for i := range o.status {
		v := &o.status[i]
		if v.Kind == kind && v.Index == index {
			v.Connected = connected
			if success {
				v.Sent++
				v.Error = ""
			} else {
				v.Failures++
				v.Error = "connection or delivery failed; inspect endpoint settings"
			}
		}
	}
}
func (o *oneBotRuntime) post(ctx context.Context, rev uint64, i int, p OneBotPost, b []byte) {
	req, e := http.NewRequestWithContext(ctx, "POST", p.URL, bytes.NewReader(b))
	if e != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	var event map[string]json.RawMessage
	_ = json.Unmarshal(b, &event)
	req.Header.Set("X-Self-ID", string(event["self_id"]))
	if p.Secret != "" {
		h := hmac.New(sha1.New, []byte(p.Secret))
		h.Write(b)
		req.Header.Set("X-Signature", "sha1="+hex.EncodeToString(h.Sum(nil)))
	}
	client := &http.Client{Timeout: time.Duration(p.Timeout) * time.Millisecond, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, e := client.Do(req)
	if e != nil {
		o.update(rev, "http_post", i, false, false)
		return
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		o.update(rev, "http_post", i, false, false)
		return
	}
	out, e := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if e != nil {
		o.update(rev, "http_post", i, false, false)
		return
	}
	o.update(rev, "http_post", i, true, true)
	if len(bytes.TrimSpace(out)) > 0 {
		var operation map[string]json.RawMessage
		if json.Unmarshal(out, &operation) == nil {
			r := o.s.quickOperation(ctx, b, out)
			if r.Status == "failed" {
				o.s.logger.Warn("OneBot quick operation failed", "retcode", r.Retcode)
			}
		}
	}
}
func (o *oneBotRuntime) reverse(ctx context.Context, rev uint64, i int, p OneBotReverse) {
	for ctx.Err() == nil {
		self, e := o.s.bot.Self(ctx)
		if e == nil && self.Uin != 0 && self.Online {
			h := http.Header{"X-Self-ID": {strconv.FormatUint(uint64(self.Uin), 10)}, "X-Client-Role": {p.Role}}
			if p.Token != "" {
				h.Set("Authorization", "Bearer "+p.Token)
			}
			d := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
			conn, res, e := d.DialContext(ctx, p.URL, h)
			if res != nil && res.Body != nil {
				res.Body.Close()
			}
			if e == nil {
				o.update(rev, "reverse_ws", i, true, true)
				o.s.runOneBotSocket(ctx, conn, p.Role)
				o.update(rev, "reverse_ws", i, false, false)
			} else {
				o.update(rev, "reverse_ws", i, false, false)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(p.ReconnectInterval) * time.Millisecond):
		}
	}
}
func (s *Server) serveOneBotWS(w http.ResponseWriter, r *http.Request) {
	role := "Universal"
	path := strings.TrimSuffix(r.URL.Path, "/")
	if strings.HasSuffix(path, "/api") {
		role = "API"
	}
	if strings.HasSuffix(path, "/event") {
		role = "Event"
	}
	u := websocket.Upgrader{HandshakeTimeout: 5 * time.Second, ReadBufferSize: 4096, WriteBufferSize: 4096}
	c, e := u.Upgrade(w, r, nil)
	if e != nil {
		return
	}
	s.runOneBotSocket(r.Context(), c, role)
}
func (s *Server) runOneBotSocket(parent context.Context, c *websocket.Conn, role string) {
	defer c.Close()
	c.SetReadLimit(8 << 20)
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	go func() {
		select {
		case <-s.ob.ctx.Done():
			cancel()
		case <-ctx.Done():
		}
		c.Close()
	}()
	var events <-chan []byte
	if role != "API" {
		ch, stop := s.ob.subscribe()
		defer stop()
		events = ch
	}
	responses := make(chan []byte, 32)
	go func() {
		defer cancel()
		for {
			typ, b, e := c.ReadMessage()
			if e != nil {
				return
			}
			if typ != websocket.TextMessage || role == "Event" {
				continue
			}
			var act obAction
			var resp obResponse
			if json.Unmarshal(b, &act) != nil || act.Action == "" {
				resp = obBad("invalid action JSON", nil)
			} else {
				callCtx, stop := context.WithTimeout(ctx, defaultCallTimeout)
				resp = s.handleOneBotAction(callCtx, act)
				stop()
			}
			out, _ := json.Marshal(resp)
			select {
			case responses <- out:
			case <-ctx.Done():
				return
			}
		}
	}()
	write := func(b []byte) error {
		_ = c.SetWriteDeadline(time.Now().Add(10 * time.Second))
		return c.WriteMessage(websocket.TextMessage, b)
	}
	if role != "API" {
		if write(s.ob.meta("lifecycle", "connect", 0)) != nil {
			return
		}
	}
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case b, ok := <-events:
			if !ok {
				return
			}
			if write(b) != nil {
				return
			}
		case b := <-responses:
			if write(b) != nil {
				return
			}
		case <-ping.C:
			if c.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)) != nil {
				return
			}
		}
	}
}

func (o *oneBotRuntime) queueEvent(b []byte) {
	if len(b) == 0 {
		return
	}
	select {
	case o.eventQueue <- b:
	case <-o.ctx.Done():
	}
}
func (o *oneBotRuntime) deliverEvents() {
	for {
		select {
		case <-o.ctx.Done():
			return
		case b := <-o.eventQueue:
			o.mu.Lock()
			c := o.config
			ctx := o.runContext
			rev := o.revision
			o.mu.Unlock()
			if ctx == nil {
				ctx = o.ctx
			}
			for i, p := range c.HTTPPost {
				if ctx.Err() != nil {
					break
				}
				o.post(ctx, rev, i, p, b)
			}
			o.publish(b)
		}
	}
}
