package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"openopq/internal/bot"
	"sort"
	"strconv"
	"strings"
	"time"

	"openopq/internal/protocol"
)

func obBad(msg string, echo json.RawMessage) obResponse {
	return obResponse{Status: "failed", Retcode: 1400, Data: nil, Message: msg, Echo: echo}
}
func obUnsupported(feature string, echo json.RawMessage) obResponse {
	return obResponse{Status: "failed", Retcode: 100, Data: nil, Message: feature + ": not supported by this QQ backend", Echo: echo, Unavailable: true}
}
func (s *Server) handleOneBotAction(ctx context.Context, act obAction) obResponse {
	if !knownAction(act.Action) {
		return obResponse{Status: "failed", Retcode: 1404, Data: nil, Message: "unknown action", Echo: act.Echo}
	}
	raw, err := normalizeOBParams(act.Params, actionBase(act.Action))
	if err != nil {
		return obBad(err.Error(), act.Echo)
	}
	act.Params = raw
	if err := validateOneBotQuery(actionBase(act.Action), raw); err != nil {
		return obBad(err.Error(), act.Echo)
	}
	if strings.HasSuffix(act.Action, "_async") || strings.HasSuffix(act.Action, "_rate_limited") {
		s.ob.queueOnce.Do(func() {
			go func() {
				var last time.Time
				for {
					select {
					case <-s.ob.ctx.Done():
						return
					case task := <-s.ob.queue:
						if strings.HasSuffix(task.Action, "_rate_limited") {
							s.ob.mu.Lock()
							interval := s.ob.config.RateLimitInterval
							s.ob.mu.Unlock()
							delay := time.Until(last.Add(time.Duration(interval) * time.Millisecond))
							if delay > 0 {
								select {
								case <-s.ob.ctx.Done():
									return
								case <-time.After(delay):
								}
							}
							last = time.Now()
						}
						task.Action = actionBase(task.Action)
						ctx, cancel := context.WithTimeout(s.ob.ctx, defaultCallTimeout)
						res := s.dispatchOneBotAction(ctx, task)
						cancel()
						if res.Status == "failed" {
							s.logger.Warn("OneBot asynchronous action failed", "action", task.Action, "retcode", res.Retcode)
						}
					}
				}
			}()
		})
		select {
		case <-s.ob.ctx.Done():
			return obFail("adapter closed", act.Echo)
		case s.ob.queue <- act:
			return obResponse{Status: "async", Retcode: 1, Data: nil, Echo: act.Echo}
		default:
			return obFail("action queue is full", act.Echo)
		}
	}
	return s.dispatchOneBotAction(ctx, act)
}
func validateSendTarget(action, kind string, user, group uint64) error {
	if user > math.MaxUint32 || group > math.MaxUint32 {
		return fmt.Errorf("QQ/group ID outside supported range")
	}
	if kind != "" && kind != "group" && kind != "private" {
		return fmt.Errorf("message_type must be group or private")
	}
	if action == "send_private_msg" {
		if user == 0 || kind == "group" {
			return fmt.Errorf("private send requires user_id and a private message_type")
		}
		return nil
	}
	if action == "send_group_msg" {
		if group == 0 || user != 0 || kind == "private" {
			return fmt.Errorf("group send requires group_id and no private target")
		}
		return nil
	}
	if kind == "group" {
		if group == 0 {
			return fmt.Errorf("group_id required")
		}
	} else if kind == "private" {
		if user == 0 {
			return fmt.Errorf("user_id required")
		}
	} else if (user == 0) == (group == 0) {
		return fmt.Errorf("specify exactly one target or message_type")
	}
	return nil
}
func validateManage(action string, group, user uint64, duration uint32) error {
	if group > math.MaxUint32 || user > math.MaxUint32 {
		return fmt.Errorf("QQ/group ID outside supported range")
	}
	if strings.HasPrefix(action, "set_group_") || action == "group_poke" || action == "send_group_poke" {
		if group == 0 {
			return fmt.Errorf("group_id required")
		}
	}
	switch action {
	case "set_group_ban", "set_group_kick", "set_group_admin", "set_group_card", "set_group_special_title", "group_poke", "send_group_poke", "send_poke", "friend_poke":
		if user == 0 {
			return fmt.Errorf("user_id required")
		}
	}
	if duration > 30*24*3600 {
		return fmt.Errorf("duration exceeds 30 days")
	}
	return nil
}

type obSegment struct {
	Type string                     `json:"type"`
	Data map[string]json.RawMessage `json:"data"`
}

func validatedOBMessage(raw json.RawMessage, escape bool, rich ...bool) (string, error) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		if escape {
			return cqEscape(text), nil
		}
		if err := validateCQ(text, rich...); err != nil {
			return "", err
		}
		if strings.TrimSpace(text) == "" {
			return "", fmt.Errorf("message is empty")
		}
		return text, nil
	}
	var segs []obSegment
	if len(raw) > 0 && raw[0] == '{' {
		var seg obSegment
		if e := json.Unmarshal(raw, &seg); e != nil {
			return "", fmt.Errorf("invalid message segment")
		}
		segs = []obSegment{seg}
	} else if e := json.Unmarshal(raw, &segs); e != nil {
		return "", fmt.Errorf("message must be a string, segment or segment array")
	}
	var b strings.Builder
	for _, seg := range segs {
		value := func(key string) string {
			var v string
			if json.Unmarshal(seg.Data[key], &v) == nil {
				return v
			}
			return string(seg.Data[key])
		}
		switch seg.Type {
		case "text":
			if _, ok := seg.Data["text"]; !ok {
				return "", fmt.Errorf("text segment requires text")
			}
			b.WriteString(cqEscape(value("text")))
		case "face":
			if len(rich) == 0 || !rich[0] {
				return "", fmt.Errorf("face sending unavailable")
			}
			id, e := strconv.ParseUint(value("id"), 10, 32)
			if e != nil {
				return "", fmt.Errorf("invalid face.id")
			}
			fmt.Fprintf(&b, "[CQ:face,id=%d]", id)
		case "at":
			q := value("qq")
			if q != "all" {
				n, e := strconv.ParseUint(q, 10, 32)
				if e != nil || n == 0 {
					return "", fmt.Errorf("invalid at.qq")
				}
			}
			fmt.Fprintf(&b, "[CQ:at,qq=%s]", q)
		case "image", "record":
			src := value("file")
			if src == "" {
				src = value("url")
			}
			if src == "" {
				return "", fmt.Errorf("media segment requires file")
			}
			for k, v := range seg.Data {
				if k == "file" || k == "url" {
					continue
				}
				var t string
				_ = json.Unmarshal(v, &t)
				if (k == "cache" || k == "proxy") && (t == "1" || string(v) == "1") {
					continue
				}
				return "", fmt.Errorf("media option %s is not implemented", k)
			}
			fmt.Fprintf(&b, "[CQ:%s,file=%s]", seg.Type, cqEscapeParam(src))
		case "reply":
			// Quoting is not implemented yet. Clients such as Dice!Next quote the message
			// they answer on every reply, so refusing the segment would refuse every reply;
			// send the rest of the message without the quote instead.
		default:
			return "", fmt.Errorf("unsupported message segment: %s", seg.Type)
		}
	}
	if e := validateCQ(b.String(), rich...); e != nil {
		return "", e
	}
	if b.Len() == 0 {
		return "", fmt.Errorf("empty message")
	}
	return b.String(), nil
}
func validateCQ(text string, rich ...bool) error {
	native := len(rich) > 0 && rich[0]
	images, records, ats, faces := 0, 0, 0, 0
	for rest := text; ; {
		i := strings.Index(rest, "[CQ:")
		if i < 0 {
			break
		}
		end := strings.Index(rest[i:], "]")
		if end < 0 {
			return fmt.Errorf("unterminated CQ segment")
		}
		p := strings.Split(rest[i+4:i+end], ",")
		rest = rest[i+end+1:]
		opts := map[string]string{}
		for _, v := range p[1:] {
			k, val, ok := strings.Cut(v, "=")
			if !ok || k == "" {
				return fmt.Errorf("malformed CQ parameter")
			}
			if _, dup := opts[k]; dup {
				return fmt.Errorf("duplicate CQ parameter")
			}
			opts[k] = cqUnescape(val)
		}
		switch p[0] {
		case "face":
			faces++
			if !native {
				return fmt.Errorf("face sending unavailable")
			}
			if _, e := strconv.ParseUint(opts["id"], 10, 32); e != nil || len(opts) != 1 {
				return fmt.Errorf("invalid face segment")
			}
		case "at":
			ats++
			q := opts["qq"]
			if q != "all" {
				n, e := strconv.ParseUint(q, 10, 32)
				if e != nil || n == 0 {
					return fmt.Errorf("invalid mention")
				}
			}
			if len(opts) != 1 {
				return fmt.Errorf("unsupported at options")
			}
		case "image", "record":
			if opts["file"] == "" && opts["url"] == "" {
				return fmt.Errorf("media requires file or URL")
			}
			for k, v := range opts {
				if k != "file" && k != "url" && !((k == "cache" || k == "proxy") && v == "1") {
					return fmt.Errorf("unsupported media option %s", k)
				}
			}
			if p[0] == "image" {
				images++
			} else {
				records++
			}
		default:
			return fmt.Errorf("unsupported CQ segment %s", p[0])
		}
	}
	if images > 20 || records > 1 || (!native && images > 1) || (records > 0 && (ats > 0 || faces > 0 || images > 0 || strings.TrimSpace(stripCQ(text)) != "")) || (!native && images > 0 && ats > 0) {
		return fmt.Errorf("this backend cannot send this mixed-media combination without loss")
	}
	return nil
}
func stripCQ(text string) string {
	var b strings.Builder
	for {
		i := strings.Index(text, "[CQ:")
		if i < 0 {
			b.WriteString(text)
			break
		}
		b.WriteString(text[:i])
		j := strings.Index(text[i:], "]")
		if j < 0 {
			break
		}
		text = text[i+j+1:]
	}
	return b.String()
}
func cqSegments(text string) []map[string]any {
	out := []map[string]any{}
	for len(text) > 0 {
		i := strings.Index(text, "[CQ:")
		if i < 0 {
			out = append(out, map[string]any{"type": "text", "data": map[string]string{"text": cqUnescape(text)}})
			break
		}
		if i > 0 {
			out = append(out, map[string]any{"type": "text", "data": map[string]string{"text": cqUnescape(text[:i])}})
		}
		j := strings.Index(text[i:], "]")
		if j < 0 {
			out = append(out, map[string]any{"type": "text", "data": map[string]string{"text": cqUnescape(text[i:])}})
			break
		}
		p := strings.Split(text[i+4:i+j], ",")
		data := map[string]string{}
		for _, opt := range p[1:] {
			k, v, _ := strings.Cut(opt, "=")
			data[k] = cqUnescape(v)
		}
		out = append(out, map[string]any{"type": p[0], "data": data})
		text = text[i+j+1:]
	}
	return out
}

type obCachedMessage struct {
	ID      int32
	Key     string
	Target  protocol.SendTarget
	Result  protocol.SendResult
	Sent    bool
	Data    map[string]any
	Created time.Time
}

func (o *oneBotRuntime) remember(m obCachedMessage) int32 {
	o.mu.Lock()
	defer o.mu.Unlock()
	if id, ok := o.keys[m.Key]; ok && time.Since(o.cache[id].Created) < time.Hour {
		old := o.cache[id]
		// A self-sent push can enrich a prior send result, without losing the
		// native receipt needed for private recall.
		m.Data["message_id"], m.Data["real_id"] = id, id
		old.Data = m.Data
		if m.Sent {
			old.Sent, old.Result = true, m.Result
		}
		o.cache[id] = old
		return id
	}
	for len(o.order) > 0 && (len(o.order) >= 1024 || time.Since(o.cache[o.order[0]].Created) > time.Hour) {
		id := o.order[0]
		delete(o.keys, o.cache[id].Key)
		delete(o.cache, id)
		o.order = o.order[1:]
	}
	o.next++
	if o.next <= 0 {
		o.next = 1
	}
	for {
		if _, exists := o.cache[o.next]; !exists {
			break
		}
		o.next++
	}
	m.ID = o.next
	m.Created = time.Now()
	m.Data["message_id"] = m.ID
	m.Data["real_id"] = m.ID
	o.cache[m.ID] = m
	o.keys[m.Key] = m.ID
	o.order = append(o.order, m.ID)
	return m.ID
}
func (o *oneBotRuntime) rememberSend(ctx context.Context, target protocol.SendTarget, result protocol.SendResult, cq string) int32 {
	self, _ := o.s.bot.Self(ctx)
	m := &protocol.IncomingMessage{IsGroup: target.Group != 0, GroupUin: target.Group, IsTemporary: target.IsTemporary(), TempGroupUin: target.TempGroup, SenderUin: self.Uin, SenderNick: self.Nickname, ToUin: target.Uin, MsgSeq: result.Sequence, MsgTime: result.Timestamp}
	var data map[string]any
	_ = json.Unmarshal(onebotEventJSON(protocol.MessageEvent{SelfUin: self.Uin, Msg: m}), &data)
	data["message"], data["raw_message"] = cq, cq
	data = cachedOneBotEvent(data)
	return o.remember(obCachedMessage{Key: oneBotMessageKey(target, self.Uin, result.Sequence, result.Timestamp), Target: target, Result: result, Sent: true, Data: data})
}
func (o *oneBotRuntime) event(ev protocol.MessageEvent) []byte {
	b := onebotEventJSON(ev)
	if b == nil {
		return nil
	}
	var v map[string]any
	_ = json.Unmarshal(b, &v)
	if ev.Msg != nil {
		m := ev.Msg
		target := protocol.SendTarget{Group: m.GroupUin}
		if !m.IsGroup {
			target = protocol.SendTarget{Uin: m.SenderUin, Uid: m.SenderUid, TempGroup: m.TempGroupUin}
			if m.SenderUin == ev.SelfUin && m.ToUin != 0 {
				target = protocol.SendTarget{Uin: m.ToUin, Uid: m.ToUid, TempGroup: m.TempGroupUin}
			}
		}
		message := any(cqMessage(m))
		if o.format() == "array" {
			message = cqSegments(cqMessage(m))
		}
		v["message"] = message
		if m.IsGroup {
			v["anonymous"] = nil
		}
		sender := v["sender"].(map[string]any)
		sender["sex"] = "unknown"
		sender["age"] = 0
		data := cachedOneBotEvent(v)
		id := o.remember(obCachedMessage{Key: oneBotMessageKey(target, m.SenderUin, m.MsgSeq, m.MsgTime), Target: target, Result: protocol.SendResult{Sequence: m.MsgSeq, Timestamp: m.MsgTime}, Data: data})
		v["message_id"] = id
	} else if ev.Notice != nil && (ev.Notice.Kind == protocol.NoticeGroupRecall || ev.Notice.Kind == protocol.NoticeFriendRecall) {
		n := ev.Notice
		o.mu.Lock()
		for i := len(o.order) - 1; i >= 0; i-- {
			m := o.cache[o.order[i]]
			if m.Result.Sequence == n.MsgSeq && m.Target.Group == n.GroupUin && (n.GroupUin != 0 || m.Target.Uin == n.UserUin) {
				v["message_id"] = m.ID
				break
			}
		}
		o.mu.Unlock()
	}
	out, _ := json.Marshal(v)
	return out
}
func (s *Server) deleteOneBotMessage(ctx context.Context, id int32, echo json.RawMessage) obResponse {
	s.ob.mu.Lock()
	m, ok := s.ob.cache[id]
	s.ob.mu.Unlock()
	if !ok || time.Since(m.Created) > time.Hour {
		return obFail("unknown or expired message_id for this account", echo)
	}
	if m.Target.IsTemporary() {
		return obUnsupported("temporary private recall has not been validated", echo)
	}
	var err error
	if m.Target.Group != 0 {
		mgr, ok := s.bot.(interface {
			RecallGroupMessage(context.Context, uint32, uint32) error
		})
		if !ok {
			return obUnsupported("group recall", echo)
		}
		err = mgr.RecallGroupMessage(ctx, m.Target.Group, m.Result.Sequence)
	} else {
		if !m.Sent {
			return obFail("cannot recall a received private message", echo)
		}
		mgr, ok := s.bot.(interface {
			RecallPrivateMessage(context.Context, protocol.SendTarget, protocol.SendResult) error
		})
		if !ok {
			return obUnsupported("private recall", echo)
		}
		err = mgr.RecallPrivateMessage(ctx, m.Target, m.Result)
	}
	if err != nil {
		return obFail(err.Error(), echo)
	}
	return obOK(nil, echo)
}

// Optional backend actions have a typed OneBot surface, never raw invented OIDB.
type oneBotBackend interface {
	OneBotAction(context.Context, string, json.RawMessage) (any, error)
}

func (s *Server) backendOneBotAction(ctx context.Context, act obAction) obResponse {
	if b, ok := s.bot.(oneBotBackend); ok {
		data, err := b.OneBotAction(ctx, act.Action, act.Params)
		if err != nil {
			if errors.Is(err, bot.ErrUnsupported) {
				return obUnsupported(act.Action, act.Echo)
			}
			return obFail(err.Error(), act.Echo)
		}
		if act.Action == "get_forward_msg" {
			if messages, ok := data.([]*protocol.IncomingMessage); ok {
				return s.oneBotForwardResponse(ctx, messages, act.Echo)
			}
		}
		return obOK(data, act.Echo)
	}
	return obUnsupported(act.Action, act.Echo)
}
func (s *Server) extraOneBotAction(ctx context.Context, act obAction) obResponse {
	switch act.Action {
	case "get_forward_msg":
		var p map[string]json.RawMessage
		_ = json.Unmarshal(act.Params, &p)
		var id string
		if v, ok := p["id"]; ok && json.Unmarshal(v, &id) != nil {
			return obBad("id must be a string", act.Echo)
		}
		if id == "" {
			if v, ok := p["message_id"]; ok {
				if json.Unmarshal(v, &id) != nil {
					var number uint64
					if json.Unmarshal(v, &number) != nil || number == 0 {
						return obBad("message_id must be a string or positive integer", act.Echo)
					}
					id = strconv.FormatUint(number, 10)
				}
				if n, err := strconv.ParseInt(id, 10, 32); err == nil {
					s.ob.mu.Lock()
					cached, exists := s.ob.cache[int32(n)]
					s.ob.mu.Unlock()
					if exists && time.Since(cached.Created) <= time.Hour {
						cq, _ := cached.Data["raw_message"].(string)
						for _, segment := range cqSegments(cq) {
							if segment["type"] == "forward" {
								id = segment["data"].(map[string]string)["id"]
								break
							}
						}
					}
				}
			}
		}
		if id == "" {
			return obBad("id or message_id is required", act.Echo)
		}
		act.Params, _ = json.Marshal(map[string]string{"id": id})
		return s.backendOneBotAction(ctx, act)
	case "clean_cache":
		res := s.backendOneBotAction(ctx, act)
		if res.Status == "ok" {
			s.ob.mu.Lock()
			s.ob.cache = map[int32]obCachedMessage{}
			s.ob.keys = map[string]int32{}
			s.ob.order = nil
			s.ob.mu.Unlock()
		}
		return res
	case "get_msg":
		var p struct {
			ID int32 `json:"message_id"`
		}
		if json.Unmarshal(act.Params, &p) != nil || p.ID == 0 {
			return obBad("message_id required", act.Echo)
		}
		s.ob.mu.Lock()
		m, ok := s.ob.cache[p.ID]
		s.ob.mu.Unlock()
		if !ok || time.Since(m.Created) > time.Hour {
			return obFail("message not in this account's bounded cache", act.Echo)
		}
		data := cachedOneBotEvent(m.Data)
		if cq, ok := data["raw_message"].(string); ok {
			data["message"] = cq
			if s.ob.format() == "array" {
				data["message"] = cqSegments(cq)
			}
		}
		return obOK(data, act.Echo)
	case "delete_msg":
		var p struct {
			ID int32 `json:"message_id"`
		}
		if json.Unmarshal(act.Params, &p) != nil || p.ID == 0 {
			return obBad("message_id required", act.Echo)
		}
		return s.deleteOneBotMessage(ctx, p.ID, act.Echo)
	case "get_group_info":
		var p struct {
			ID uint32 `json:"group_id"`
		}
		if json.Unmarshal(act.Params, &p) != nil || p.ID == 0 {
			return obBad("group_id required", act.Echo)
		}
		groups, e := s.bot.Groups(ctx)
		if e != nil {
			return obFail(e.Error(), act.Echo)
		}
		for _, g := range groups {
			if g.GroupUin == p.ID {
				return obOK(oneBotGroupInfo(g), act.Echo)
			}
		}
		return obFail("group not found", act.Echo)
	case "can_send_image":
		_, ok := s.bot.(imageSender)
		return obOK(map[string]bool{"yes": ok}, act.Echo)
	case "can_send_record":
		_, ok := s.bot.(voiceSender)
		return obOK(map[string]bool{"yes": ok}, act.Echo)
	case "get_supported_actions":
		return obOK(s.supportedOneBotActions(), act.Echo)
	case "get_onebot_capabilities":
		supported := s.supportedOneBotActions()
		set := map[string]bool{}
		for _, a := range supported {
			set[a] = true
		}
		unavailable := []string{}
		for _, a := range oneBotActions {
			if !set[a] {
				unavailable = append(unavailable, a)
			}
		}
		_, temporary := s.bot.(interface {
			PrepareSendTarget(context.Context, protocol.SendTarget) (protocol.SendTarget, error)
		})
		return obOK(map[string]any{"supported": supported, "unavailable": unavailable, "transports": []string{"http", "http_post", "websocket", "reverse_websocket"}, "temporary_private": map[string]any{"supported": temporary, "source_group_parameter": "group_id", "implicit_reply": "only one unambiguous received temporary context", "recall": false}, "message_segments": []string{"text", "at", "face", "image", "record", "forward", "node", "xml", "json"}, "limitations": []string{"message cache: 1024 entries / one hour / current account only", "native sends preserve text/mentions/faces/up to 20 images; record must be a separate message", "temporary private sends require a source group and remain subject to QQ permissions; no C2C fallback", "audio conversion requires media tools; received media is account scoped and expires after one hour", "backend availability and QQ permissions are checked on each call"}}, act.Echo)
	case ".handle_quick_operation":
		var p struct {
			Context   json.RawMessage `json:"context"`
			Operation json.RawMessage `json:"operation"`
		}
		if json.Unmarshal(act.Params, &p) != nil {
			return obFail("invalid quick operation", act.Echo)
		}
		r := s.quickOperation(ctx, p.Context, p.Operation)
		r.Echo = act.Echo
		return r
	default:
		return s.backendOneBotAction(ctx, act)
	}
}
func (s *Server) quickOperation(ctx context.Context, event, operation json.RawMessage) obResponse {
	var e struct {
		SelfID      uint32 `json:"self_id"`
		PostType    string `json:"post_type"`
		MessageType string `json:"message_type"`
		UserID      uint32 `json:"user_id"`
		GroupID     uint32 `json:"group_id"`
		Sender      struct {
			GroupID uint32 `json:"group_id"`
		} `json:"sender"`
		MessageID   int32  `json:"message_id"`
		Flag        string `json:"flag"`
		RequestType string `json:"request_type"`
		SubType     string `json:"sub_type"`
	}
	var op map[string]json.RawMessage
	if json.Unmarshal(event, &e) != nil || json.Unmarshal(operation, &op) != nil || op == nil {
		return obFail("invalid quick operation", nil)
	}
	self, err := s.bot.Self(ctx)
	if err != nil || e.SelfID != self.Uin {
		return obFail("quick operation account mismatch", nil)
	}
	allowed := map[string]bool{}
	if e.PostType == "message" {
		for _, k := range []string{"reply", "auto_escape", "at_sender", "delete", "kick", "ban", "ban_duration", "reject_add_request"} {
			allowed[k] = true
		}
	} else if e.PostType == "request" {
		for _, k := range []string{"approve", "remark", "reason"} {
			allowed[k] = true
		}
	} else {
		return obFail("event has no quick operations", nil)
	}
	for k := range op {
		if !allowed[k] {
			return obFail("unsupported quick operation: "+k, nil)
		}
	}
	call := func(action string, params map[string]any) obResponse {
		raw, _ := json.Marshal(params)
		return s.handleOneBotAction(ctx, obAction{Action: action, Params: raw})
	}
	for _, k := range []string{"auto_escape", "at_sender", "delete", "kick", "ban", "reject_add_request", "approve"} {
		if value, ok := op[k]; ok {
			var valid bool
			if json.Unmarshal(value, &valid) != nil {
				return obBad("quick operation requires boolean "+k, nil)
			}
		}
	}
	flag := func(k string, def bool) bool {
		v, ok := op[k]
		if !ok {
			return def
		}
		var b bool
		_ = json.Unmarshal(v, &b)
		return b
	}
	if reply, ok := op["reply"]; ok {
		if e.MessageType != "group" && e.MessageType != "private" {
			return obFail("invalid message type", nil)
		}
		_, rich := s.bot.(interface {
			SendRichMessage(context.Context, protocol.SendTarget, []protocol.SendSegment) (protocol.SendResult, error)
		})
		cq, err := validatedOBMessage(reply, flag("auto_escape", false), rich)
		if err != nil {
			return obFail(err.Error(), nil)
		}
		if e.MessageType == "group" && flag("at_sender", true) {
			cq = fmt.Sprintf("[CQ:at,qq=%d]", e.UserID) + cq
		}
		group := e.GroupID
		if e.MessageType == "private" && e.SubType == "group" {
			group = e.Sender.GroupID
			if group == 0 {
				return obBad("temporary quick reply requires sender.group_id", nil)
			}
		}
		r := call("send_msg", map[string]any{"message_type": e.MessageType, "group_id": group, "user_id": e.UserID, "message": cq})
		if r.Status != "ok" {
			return r
		}
	}
	if e.PostType == "message" {
		if flag("delete", false) {
			r := call("delete_msg", map[string]any{"message_id": e.MessageID})
			if r.Status != "ok" {
				return r
			}
		}
		for _, action := range []struct{ key, name string }{{"kick", "set_group_kick"}, {"ban", "set_group_ban"}} {
			if flag(action.key, false) {
				if e.MessageType != "group" {
					return obFail("group operation requires group message", nil)
				}
				duration := json.RawMessage(`1800`)
				if v, ok := op["ban_duration"]; ok {
					duration = v
				}
				r := call(action.name, map[string]any{"group_id": e.GroupID, "user_id": e.UserID, "duration": duration, "reject_add_request": flag("reject_add_request", false)})
				if r.Status != "ok" {
					return r
				}
			}
		}
	}
	if _, ok := op["approve"]; ok && e.PostType == "request" {
		params := map[string]any{"flag": e.Flag, "approve": flag("approve", true), "sub_type": e.SubType}
		if v, ok := op["remark"]; ok {
			params["remark"] = v
		}
		if v, ok := op["reason"]; ok {
			params["reason"] = v
		}
		return call("set_"+e.RequestType+"_add_request", params)
	}
	return obOK(nil, nil)
}

func (s *Server) supportedOneBotActions() []string {
	out := []string{"get_login_info", "get_group_list", "get_group_info", "get_status", "get_version_info", "get_msg", "can_send_image", "can_send_record", ".handle_quick_operation", "get_supported_actions", "get_onebot_capabilities"}
	if _, ok := s.bot.(messageSender); ok {
		out = append(out, "send_msg", "send_group_msg", "send_private_msg")
	}
	if _, ok := s.bot.(interface {
		RecallGroupMessage(context.Context, uint32, uint32) error
	}); ok {
		out = append(out, "delete_msg")
	}
	if _, ok := s.bot.(groupActions); ok {
		out = append(out, "set_group_ban", "set_group_whole_ban", "set_group_kick", "set_group_admin", "set_group_card", "set_group_name", "set_group_leave", "set_group_special_title", "group_poke", "send_group_poke", "send_poke", "friend_poke")
	}
	if b, ok := s.bot.(interface{ SupportedOneBotActions() []string }); ok {
		out = append(out, b.SupportedOneBotActions()...)
	}
	sort.Strings(out)
	return out
}
