package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"log/slog"
	"openopq/internal/bot"
	"openopq/internal/buildinfo"
)

type OB12Self struct {
	Platform string `json:"platform"`
	UserID   string `json:"user_id"`
}
type ob12Action struct {
	Action string          `json:"action"`
	Params json.RawMessage `json:"params"`
	Self   *OB12Self       `json:"self,omitempty"`
	Echo   string          `json:"echo,omitempty"`
}
type ob12Response struct {
	Status  string `json:"status"`
	Retcode int    `json:"retcode"`
	Data    any    `json:"data"`
	Message string `json:"message"`
	Echo    string `json:"echo,omitempty"`
}
type ob12Error struct {
	code    int
	message string
}

func (e *ob12Error) Error() string         { return e.message }
func bad12(code int, message string) error { return &ob12Error{code, message} }
func fail12(e error, echo string) ob12Response {
	code := 34000
	if v, ok := e.(*ob12Error); ok {
		code = v.code
	}
	return ob12Response{"failed", code, nil, e.Error(), echo}
}
func ok12(data any, echo string) ob12Response { return ob12Response{"ok", 0, data, "", echo} }
func id12() string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func version12() map[string]any {
	return map[string]any{"impl": "openopq", "version": buildinfo.Version, "onebot_version": "12"}
}
func IsOneBot12Path(path string) bool {
	return path == "/onebot/v12" || strings.HasPrefix(path, "/onebot/v12/")
}

type ob12TransportKey struct{}

func OneBot12Transport(ctx context.Context) string {
	s, _ := ctx.Value(ob12TransportKey{}).(string)
	return s
}
func (s *Server) peers12(ctx context.Context) []*Server {
	if s.opts.OneBot12Peers != nil {
		return s.opts.OneBot12Peers(ctx)
	}
	return []*Server{s}
}
func (s *Server) own12(ctx context.Context) OB12Self {
	v, _ := s.bot.Self(ctx)
	id := strconv.FormatUint(uint64(v.Uin), 10)
	if v.Uin == 0 && s.opts.OneBot12UserID != "" {
		id = s.opts.OneBot12UserID
	}
	return OB12Self{"qq", id}
}
func (s *Server) status12(ctx context.Context) map[string]any {
	bots := []map[string]any{}
	good := true
	for _, p := range s.peers12(ctx) {
		own, e := p.bot.Self(ctx)
		identity := p.own12(ctx)
		if identity.UserID == "0" || identity.UserID == "" {
			continue
		}
		online := e == nil && own.Online
		good = good && online
		bots = append(bots, map[string]any{"self": identity, "online": online})
	}
	return map[string]any{"good": good, "bots": bots}
}
func (s *Server) select12(ctx context.Context, self *OB12Self) (*Server, error) {
	peers := s.peers12(ctx)
	if self == nil {
		if len(peers) == 1 {
			return peers[0], nil
		}
		return nil, bad12(10101, "self is required on a multi-account connection")
	}
	for _, p := range peers {
		if p.own12(ctx) == *self && self.Platform == "qq" && self.UserID != "0" {
			return p, nil
		}
	}
	return nil, bad12(10102, "unknown self on this connection")
}
func parse12(data []byte) (a ob12Action, err error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || fields == nil {
		return a, bad12(10001, "action request must be an object")
	}
	if _, ok := fields["params"]; !ok {
		return a, bad12(10001, "params is required")
	}
	var params map[string]json.RawMessage
	if json.Unmarshal(fields["params"], &params) != nil || params == nil {
		return a, bad12(10001, "params must be an object")
	}
	if raw, ok := fields["echo"]; ok && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return a, bad12(10001, "echo must be a string")
	}
	if json.Unmarshal(data, &a) != nil || a.Action == "" {
		return a, bad12(10001, "action and echo must be strings; self must be an object")
	}
	if raw, ok := fields["self"]; ok && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return a, bad12(10001, "self cannot be null")
	}
	return a, nil
}
func (s *Server) execute12(ctx context.Context, data []byte) ob12Response {
	s.v12.actions.Add(1)
	a, e := parse12(data)
	if e != nil {
		return fail12(e, a.Echo)
	}
	return s.dispatch12(ctx, a)
}
func (s *Server) dispatch12(ctx context.Context, a ob12Action) ob12Response {
	switch a.Action {
	case "get_version":
		return ok12(version12(), a.Echo)
	case "get_status":
		return ok12(s.status12(ctx), a.Echo)
	case "get_supported_actions":
		return ok12(s.actions12(ctx), a.Echo)
	case "get_latest_events":
		return s.v12.latest(ctx, a)
	}
	target, e := s.select12(ctx, a.Self)
	if e != nil {
		return fail12(e, a.Echo)
	}
	if target != s {
		target.v12.actions.Add(1)
	}
	out, e := target.action12(ctx, a)
	if e != nil {
		return fail12(e, a.Echo)
	}
	return ok12(out, a.Echo)
}
func (s *Server) actions12(ctx context.Context) []string {
	out := []string{"get_latest_events", "get_supported_actions", "get_status", "get_version", "upload_file", "upload_file_fragmented", "get_file", "get_file_fragmented"}
	set := map[string]bool{}
	for _, p := range s.peers12(ctx) {
		for _, name := range p.supportedOneBotActions() {
			set[name] = true
		}
	}
	for _, v := range []struct{ old, new string }{{"get_login_info", "get_self_info"}, {"get_stranger_info", "get_user_info"}, {"get_friend_list", "get_friend_list"}, {"get_group_list", "get_group_list"}, {"get_group_info", "get_group_info"}, {"get_group_member_list", "get_group_member_list"}, {"get_group_member_info", "get_group_member_info"}, {"set_group_name", "set_group_name"}, {"set_group_leave", "leave_group"}, {"send_msg", "send_message"}, {"delete_msg", "delete_message"}} {
		if set[v.old] {
			out = append(out, v.new)
		}
	}
	for _, name := range []string{"get_forward_msg", "get_group_system_msg", "set_friend_add_request", "set_group_add_request", "get_msg"} {
		if set[name] {
			out = append(out, "qq."+name)
		}
	}
	return out
}
func (s *Server) serve12(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/onebot/v12" && r.URL.Path != "/onebot/v12/" {
		http.NotFound(w, r)
		return
	}
	ws := strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
	kind := "http"
	if ws {
		kind = "ws"
	}
	r = r.WithContext(context.WithValue(r.Context(), ob12TransportKey{}, kind))
	if ws {
		s.v12.serveWS(w, r)
		return
	}
	if r.Method != "POST" {
		w.WriteHeader(405)
		return
	}
	ct, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || ct != "application/json" {
		w.WriteHeader(415)
		return
	}
	data, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 45<<20))
	var reply ob12Response
	if e != nil {
		reply = fail12(bad12(10001, "invalid or oversized JSON request"), "")
	} else {
		reply = s.execute12(r.Context(), data)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(reply)
}
func NewOneBot12Gateway(peers func(context.Context) []*Server, logger *slog.Logger, c OneBot12Config) *Server {
	c.Webhooks = nil
	c.ReverseWS = nil
	return NewServer(bot.NewStub(), logger, Options{OneBot12: true, OneBot12Peers: peers, OneBotConfig: OneBotConfig{V12: c}})
}
func decodeParams12(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var p map[string]json.RawMessage
	e := json.Unmarshal(raw, &p)
	if e != nil || p == nil {
		return nil, bad12(10003, "params must be an object")
	}
	return p, nil
}
func stringParam12(p map[string]json.RawMessage, k string, required bool) (string, error) {
	v, ok := p[k]
	if !ok && !required {
		return "", nil
	}
	var s string
	if !ok || json.Unmarshal(v, &s) != nil || (required && s == "") {
		return "", bad12(10003, k+" must be a nonempty string")
	}
	return s, nil
}
func qqParam12(p map[string]json.RawMessage, k string) (uint32, error) {
	s, e := stringParam12(p, k, true)
	if e != nil {
		return 0, e
	}
	n, e := strconv.ParseUint(s, 10, 32)
	if e != nil || n == 0 || strconv.FormatUint(n, 10) != s {
		return 0, bad12(10003, "invalid QQ "+k)
	}
	return uint32(n), nil
}
func (s *Server) call11For12(ctx context.Context, action string, p any) (any, error) {
	raw, _ := json.Marshal(p)
	r := s.handleOneBotAction(ctx, obAction{Action: action, Params: raw})
	if r.Status != "ok" {
		code := 34000
		if r.Retcode == 1400 {
			code = 10003
		}
		if r.Unavailable || r.Retcode == 1404 {
			code = 10002
		}
		return nil, bad12(code, r.Message)
	}
	return r.Data, nil
}
func map12(v any) map[string]any {
	b, _ := json.Marshal(v)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}
func list12(v any) []map[string]any {
	b, _ := json.Marshal(v)
	var m []map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}
func numberString12(v any) string { return decimal12(v) }
func user12(v map[string]any, member bool) map[string]any {
	display := ""
	if member {
		display, _ = v["card"].(string)
	}
	m := map[string]any{"user_id": numberString12(v["user_id"]), "user_name": v["nickname"], "user_displayname": display}
	if !member {
		remark, _ := v["remark"].(string)
		m["user_remark"] = remark
	}
	return m
}
func (s *Server) action12(ctx context.Context, a ob12Action) (any, error) {
	p, e := decodeParams12(a.Params)
	if e != nil {
		return nil, e
	}
	arg := map[string]any{}
	switch a.Action {
	case "upload_file", "upload_file_fragmented", "get_file", "get_file_fragmented":
		return s.v12.fileAction(ctx, a.Action, p)
	case "send_message":
		return s.send12(ctx, p)
	case "delete_message":
		id, e := stringParam12(p, "message_id", true)
		if e != nil {
			return nil, e
		}
		n, e := strconv.ParseInt(id, 10, 32)
		if e != nil {
			return nil, bad12(10003, "invalid message_id")
		}
		return s.call11For12(ctx, "delete_msg", map[string]any{"message_id": n})
	case "get_self_info":
		v, e := s.call11For12(ctx, "get_login_info", arg)
		if e != nil {
			return nil, e
		}
		m := user12(map12(v), false)
		delete(m, "user_remark")
		return m, nil
	case "get_user_info":
		id, e := qqParam12(p, "user_id")
		if e != nil {
			return nil, e
		}
		v, e := s.call11For12(ctx, "get_friend_list", arg)
		if e == nil {
			for _, m := range list12(v) {
				if numberString12(m["user_id"]) == strconv.FormatUint(uint64(id), 10) {
					return user12(m, false), nil
				}
			}
		}
		v, e = s.call11For12(ctx, "get_stranger_info", map[string]any{"user_id": id})
		if e != nil {
			return nil, e
		}
		return user12(map12(v), false), nil
	case "get_friend_list":
		v, e := s.call11For12(ctx, a.Action, arg)
		if e != nil {
			return nil, e
		}
		out := []map[string]any{}
		for _, m := range list12(v) {
			out = append(out, user12(m, false))
		}
		return out, nil
	case "get_group_list", "get_group_info", "get_group_member_list", "get_group_member_info", "set_group_name", "leave_group":
		if a.Action != "get_group_list" {
			group, e := qqParam12(p, "group_id")
			if e != nil {
				return nil, e
			}
			arg["group_id"] = group
		}
		if a.Action == "get_group_member_info" {
			user, e := qqParam12(p, "user_id")
			if e != nil {
				return nil, e
			}
			arg["user_id"] = user
		}
		if a.Action == "set_group_name" {
			name, e := stringParam12(p, "group_name", true)
			if e != nil {
				return nil, e
			}
			arg["group_name"] = name
		}
		action := a.Action
		if action == "leave_group" {
			action = "set_group_leave"
		}
		v, e := s.call11For12(ctx, action, arg)
		if e != nil {
			return nil, e
		}
		if a.Action == "set_group_name" || a.Action == "leave_group" {
			return nil, nil
		}
		if strings.Contains(a.Action, "member") {
			if a.Action == "get_group_member_info" {
				return user12(map12(v), true), nil
			}
			out := []map[string]any{}
			for _, m := range list12(v) {
				out = append(out, user12(m, true))
			}
			return out, nil
		}
		group := func(m map[string]any) map[string]any {
			return map[string]any{"group_id": numberString12(m["group_id"]), "group_name": m["group_name"]}
		}
		if a.Action == "get_group_info" {
			return group(map12(v)), nil
		}
		out := []map[string]any{}
		for _, m := range list12(v) {
			out = append(out, group(m))
		}
		return out, nil
	default:
		if strings.HasPrefix(a.Action, "qq.") {
			name := strings.TrimPrefix(a.Action, "qq.")
			for _, allowed := range []string{"get_forward_msg", "get_group_system_msg", "set_friend_add_request", "set_group_add_request", "get_msg"} {
				if name == allowed {
					return s.call11For12(ctx, name, p)
				}
			}
		}
		return nil, bad12(10002, "unsupported action: "+a.Action)
	}
}
func (s *Server) send12(ctx context.Context, p map[string]json.RawMessage) (any, error) {
	kind, e := stringParam12(p, "detail_type", true)
	if e != nil {
		return nil, e
	}
	if kind != "private" && kind != "group" {
		return nil, bad12(10004, "this QQ adapter supports private and group messages")
	}
	key := "user_id"
	if kind == "group" {
		key = "group_id"
	}
	id, e := qqParam12(p, key)
	if e != nil {
		return nil, e
	}
	tempGroup := uint32(0)
	if _, exists := p["qq.temp_group_id"]; exists {
		if kind != "private" {
			return nil, bad12(10003, "qq.temp_group_id is only valid for private messages")
		}
		var err error
		tempGroup, err = qqParam12(p, "qq.temp_group_id")
		if err != nil {
			return nil, err
		}
	}
	var segments []struct {
		Type string                     `json:"type"`
		Data map[string]json.RawMessage `json:"data"`
	}
	if json.Unmarshal(p["message"], &segments) != nil || len(segments) == 0 {
		return nil, bad12(10003, "message must be a nonempty segment array")
	}
	out := []map[string]any{}
	for _, seg := range segments {
		data := map[string]any{}
		typ := seg.Type
		switch typ {
		case "text":
			v, e := stringParam12(seg.Data, "text", false)
			if e != nil {
				return nil, bad12(10006, e.Error())
			}
			if _, ok := seg.Data["text"]; !ok {
				return nil, bad12(10006, "text is required")
			}
			data["text"] = v
		case "mention":
			n, e := qqParam12(seg.Data, "user_id")
			if e != nil {
				return nil, bad12(10006, e.Error())
			}
			typ = "at"
			data["qq"] = strconv.FormatUint(uint64(n), 10)
		case "mention_all":
			typ = "at"
			data["qq"] = "all"
		case "image", "voice":
			file, e := stringParam12(seg.Data, "file_id", true)
			if e != nil {
				return nil, bad12(10006, e.Error())
			}
			path, e := s.v12.resolveFile(ctx, file)
			if e != nil {
				return nil, e
			}
			data["file"] = path
			if typ == "voice" {
				typ = "record"
			}
		case "qq.face":
			v, e := stringParam12(seg.Data, "id", true)
			if e != nil {
				return nil, bad12(10006, e.Error())
			}
			typ = "face"
			data["id"] = v
		default:
			return nil, bad12(10005, "unsupported segment: "+seg.Type)
		}
		out = append(out, map[string]any{"type": typ, "data": data})
	}
	params := map[string]any{"message_type": kind, key: id, "message": out}
	if tempGroup != 0 {
		params["group_id"] = tempGroup
	}
	v, e := s.call11For12(ctx, "send_msg", params)
	if e != nil {
		return nil, e
	}
	m := map12(v)
	messageID := numberString12(m["message_id"])
	s.ob.mu.Lock()
	n, _ := strconv.ParseInt(messageID, 10, 32)
	cached := s.ob.cache[int32(n)]
	s.ob.mu.Unlock()
	stamp := float64(cached.Result.Timestamp)
	if stamp == 0 {
		stamp = float64(time.Now().UnixNano()) / 1e9
	}
	return map[string]any{"message_id": messageID, "time": stamp}, nil
}

func OneBot12WithTransport(ctx context.Context, kind string) context.Context {
	return context.WithValue(ctx, ob12TransportKey{}, kind)
}
