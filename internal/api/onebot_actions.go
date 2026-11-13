package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"openopq/internal/bot"
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
		case "reply":
			id, e := strconv.ParseInt(value("id"), 10, 32)
			if e != nil || id <= 0 || len(seg.Data) != 1 {
				return "", fmt.Errorf("reply requires a positive message id only")
			}
			fmt.Fprintf(&b, "[CQ:reply,id=%d]", id)
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
	images, records, ats, faces, replies := 0, 0, 0, 0, 0
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
		case "reply":
			replies++
			id, e := strconv.ParseInt(opts["id"], 10, 32)
			if !native || e != nil || id <= 0 || len(opts) != 1 || replies > 1 {
				return fmt.Errorf("invalid or unavailable reply")
			}
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
	recordWithOthers := records > 0 && (ats > 0 || faces > 0 || images > 0 || strings.TrimSpace(stripCQ(text)) != "")
	if images > 20 || records > 1 || (!native && images > 1) || recordWithOthers || (records > 0 && replies > 0 && !native) || (!native && images > 0 && ats > 0) {
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
	Reply   *protocol.SendReply
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
		if m.Reply != nil {
			old.Reply = m.Reply
		}
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
	return o.remember(obCachedMessage{Key: oneBotMessageKey(target, self.Uin, result.Sequence, result.Timestamp), Target: target, Result: result, Sent: true, Data: data, Reply: &protocol.SendReply{Sequence: result.Sequence, Timestamp: result.Timestamp, SenderUin: self.Uin, Elements: [][]byte{protocol.TextElem(stripCQ(cq))}}})
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
		cq := cqMessage(m)
		if m.ReplyTo != nil {
			quoteID := o.rememberQuote(target, *m.ReplyTo)
			cq = fmt.Sprintf("[CQ:reply,id=%d]", quoteID) + cq
		}
		v["raw_message"] = cq
		message := any(cq)
		if o.format() == "array" {
			message = cqSegments(cq)
		}
		v["message"] = message
		if m.IsGroup {
			v["anonymous"] = nil
		}
		sender := v["sender"].(map[string]any)
		sender["sex"] = "unknown"
		sender["age"] = 0
		data := cachedOneBotEvent(v)
		elems := m.RichElements
		if len(elems) == 0 {
			elems = [][]byte{protocol.TextElem(m.Content)}
		}
		id := o.remember(obCachedMessage{Key: oneBotMessageKey(target, m.SenderUin, m.MsgSeq, m.MsgTime), Target: target, Result: protocol.SendResult{Sequence: m.MsgSeq, Timestamp: m.MsgTime, Random: m.MsgRandom}, Data: data, Reply: &protocol.SendReply{Sequence: m.MsgSeq, SenderUin: m.SenderUin, SenderUid: m.SenderUid, Timestamp: m.MsgTime, Elements: elems}})
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
		return s.forwardForAgent(ctx, act)
	case "mark_msg_as_read":
		return s.markOneBotMessageRead(ctx, act)
	case "mark_group_msg_as_read":
		return s.markOneBotConversationRead(ctx, act, true)
	case "mark_private_msg_as_read":
		return s.markOneBotConversationRead(ctx, act, false)
	case "_mark_all_as_read":
		return s.markAllOneBotRead(ctx, act)
	case "get_group_msg_history", "get_friend_msg_history":
		return s.oneBotHistoryAction(ctx, act)
	case "set_essence_msg", "delete_essence_msg":
		return s.oneBotEssence(ctx, act)
	case "set_msg_emoji_like":
		return s.oneBotEmojiLike(ctx, act)
	case "send_forward_msg", "send_group_forward_msg", "send_private_forward_msg":
		return s.oneBotSendForward(ctx, act)
	case "forward_friend_single_msg", "forward_group_single_msg":
		return s.oneBotForwardSingle(ctx, act)
	case "set_group_kick_members":
		return s.oneBotKickMembers(ctx, act)
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
		return obOK(map[string]any{"supported": supported, "unavailable": unavailable, "transports": []string{"http", "http_post", "websocket", "reverse_websocket"}, "temporary_private": map[string]any{"supported": temporary, "source_group_parameter": "group_id", "implicit_reply": "only one unambiguous received temporary context", "recall": false}, "message_segments": []string{"text", "reply", "at", "face", "image", "record", "forward", "node", "xml", "json"}, "limitations": []string{"message cache: 1024 entries / one hour / current account only", "native sends preserve quoted replies/text/mentions/faces/record/up to 20 images", "temporary private sends require a source group and remain subject to QQ permissions; no C2C fallback", "audio conversion requires media tools; received media is account scoped and expires after one hour", "group history needs a message_seq or a cached group message and fetches at most 28 messages; friend history is roam-by-time", "ocr needs an http(s) image URL QQ can fetch", "emoji like is group-only and needs a cached message", "private file URL needs file_id and file_hash", "trans_group_file posts an existing group file; it does not upload", "get_online_clients reports this session only", "_mark_all_as_read covers conversations still in the message cache, at most 50", "backend availability and QQ permissions are checked on each call"}}, act.Echo)
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

func (o *oneBotRuntime) latestCached(group, user uint32) (seq, timestamp uint32) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for i := len(o.order) - 1; i >= 0; i-- {
		m := o.cache[o.order[i]]
		if time.Since(m.Created) > time.Hour {
			continue
		}
		if group != 0 {
			if m.Target.Group == group {
				return m.Result.Sequence, m.Result.Timestamp
			}
			continue
		}
		if m.Target.Group == 0 && m.Target.Uin == user {
			return m.Result.Sequence, m.Result.Timestamp
		}
	}
	return 0, 0
}

func (s *Server) markOneBotMessageRead(ctx context.Context, act obAction) obResponse {
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
	params := map[string]any{"message_seq": m.Result.Sequence, "time": m.Result.Timestamp}
	action := "mark_private_msg_as_read"
	if m.Target.Group != 0 {
		params["group_id"] = m.Target.Group
		action = "mark_group_msg_as_read"
	} else {
		params["user_id"] = m.Target.Uin
	}
	raw, _ := json.Marshal(params)
	return s.backendOneBotAction(ctx, obAction{Action: action, Params: raw, Echo: act.Echo})
}

func (s *Server) markOneBotConversationRead(ctx context.Context, act obAction, group bool) obResponse {
	var p struct {
		GroupID uint32 `json:"group_id"`
		UserID  uint32 `json:"user_id"`
		Seq     uint32 `json:"message_seq"`
		Time    uint32 `json:"time"`
	}
	if json.Unmarshal(act.Params, &p) != nil {
		return obBad("invalid parameters", act.Echo)
	}
	if p.Seq == 0 {
		if group {
			p.Seq, p.Time = s.ob.latestCached(p.GroupID, 0)
		} else {
			p.Seq, p.Time = s.ob.latestCached(0, p.UserID)
		}
	}
	if p.Seq == 0 {
		return obFail("no recent message for this conversation", act.Echo)
	}
	raw, _ := json.Marshal(map[string]any{
		"group_id": p.GroupID, "user_id": p.UserID, "message_seq": p.Seq, "time": p.Time,
	})
	act.Params = raw
	return s.backendOneBotAction(ctx, act)
}

func (s *Server) markAllOneBotRead(ctx context.Context, act obAction) obResponse {
	type conv struct {
		group      bool
		id         uint32
		seq, stamp uint32
	}
	seen := map[string]bool{}
	jobs := []conv{}
	s.ob.mu.Lock()
	for i := len(s.ob.order) - 1; i >= 0 && len(jobs) < 50; i-- {
		m := s.ob.cache[s.ob.order[i]]
		if time.Since(m.Created) > time.Hour || m.Result.Sequence == 0 {
			continue
		}
		key := fmt.Sprintf("u:%d", m.Target.Uin)
		id := m.Target.Uin
		if m.Target.Group != 0 {
			key = fmt.Sprintf("g:%d", m.Target.Group)
			id = m.Target.Group
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		jobs = append(jobs, conv{group: m.Target.Group != 0, id: id, seq: m.Result.Sequence, stamp: m.Result.Timestamp})
	}
	s.ob.mu.Unlock()
	for _, j := range jobs {
		action := "mark_private_msg_as_read"
		params := map[string]any{"message_seq": j.seq, "time": j.stamp}
		if j.group {
			action = "mark_group_msg_as_read"
			params["group_id"] = j.id
		} else {
			params["user_id"] = j.id
		}
		raw, _ := json.Marshal(params)
		res := s.backendOneBotAction(ctx, obAction{Action: action, Params: raw, Echo: act.Echo})
		if res.Status != "ok" {
			return res
		}
	}
	return obOK(true, act.Echo)
}

func (s *Server) oneBotHistoryAction(ctx context.Context, act obAction) obResponse {
	var p struct {
		GroupID uint32 `json:"group_id"`
		UserID  uint32 `json:"user_id"`
		Seq     uint32 `json:"message_seq"`
		Time    uint32 `json:"time"`
		Count   uint32 `json:"count"`
	}
	if json.Unmarshal(act.Params, &p) != nil {
		return obBad("invalid parameters", act.Echo)
	}
	if act.Action == "get_group_msg_history" && p.Seq == 0 {
		p.Seq, _ = s.ob.latestCached(p.GroupID, 0)
	}
	if act.Action == "get_friend_msg_history" && p.Time == 0 {
		_, p.Time = s.ob.latestCached(0, p.UserID)
	}
	raw, _ := json.Marshal(map[string]any{
		"group_id": p.GroupID, "user_id": p.UserID, "message_seq": p.Seq, "time": p.Time, "count": p.Count,
	})
	act.Params = raw
	res := s.backendOneBotAction(ctx, act)
	if res.Status != "ok" {
		return res
	}
	messages, ok := res.Data.([]*protocol.IncomingMessage)
	if !ok {
		return res
	}
	return s.oneBotHistoryResponse(ctx, messages, act.Echo)
}

func (s *Server) oneBotHistoryResponse(ctx context.Context, messages []*protocol.IncomingMessage, echo json.RawMessage) obResponse {
	self, err := s.bot.Self(ctx)
	if err != nil {
		return obFail(err.Error(), echo)
	}
	out := make([]map[string]any, 0, len(messages))
	for _, m := range messages {
		if m == nil {
			continue
		}
		raw := s.ob.event(protocol.MessageEvent{SelfUin: self.Uin, Msg: m})
		if raw == nil {
			continue
		}
		var event map[string]any
		if json.Unmarshal(raw, &event) != nil {
			return obFail("invalid history message", echo)
		}
		out = append(out, cachedOneBotEvent(event))
	}
	return obOK(map[string]any{"messages": out}, echo)
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
	out := []string{"get_login_info", "get_group_list", "get_group_info", "get_status", "get_version_info", "get_msg", "_mark_all_as_read", "can_send_image", "can_send_record", ".handle_quick_operation", "get_supported_actions", "get_onebot_capabilities"}
	if _, ok := s.bot.(messageSender); ok {
		out = append(out, "send_msg", "send_group_msg", "send_private_msg")
	}
	if _, ok := s.bot.(interface {
		RecallGroupMessage(context.Context, uint32, uint32) error
	}); ok {
		out = append(out, "delete_msg")
	}
	if _, ok := s.bot.(groupActions); ok {
		out = append(out, "set_group_ban", "set_group_whole_ban", "set_group_kick", "set_group_kick_members", "set_group_admin", "set_group_card", "set_group_name", "set_group_leave", "set_group_special_title", "group_poke", "send_group_poke", "send_poke", "friend_poke")
	}
	if b, ok := s.bot.(interface{ SupportedOneBotActions() []string }); ok {
		out = append(out, b.SupportedOneBotActions()...)
	}
	if _, ok := s.bot.(interface {
		SetEssenceMessage(context.Context, uint32, uint32, uint32, bool) error
	}); ok {
		out = append(out, "set_essence_msg", "delete_essence_msg")
	}
	if _, ok := s.bot.(interface {
		SetGroupReaction(context.Context, uint32, uint32, string, bool) error
	}); ok {
		out = append(out, "set_msg_emoji_like")
	}
	if _, ok := s.bot.(interface {
		SendForwardMessage(context.Context, protocol.SendTarget, []protocol.ForwardNode) (protocol.SendResult, error)
	}); ok {
		out = append(out, "send_forward_msg", "send_group_forward_msg", "send_private_forward_msg", "forward_friend_single_msg", "forward_group_single_msg")
	}
	sort.Strings(out)
	return out
}

func (s *Server) oneBotEmojiLike(ctx context.Context, act obAction) obResponse {
	var p struct {
		ID      int32           `json:"message_id"`
		EmojiID json.RawMessage `json:"emoji_id"`
		Set     *bool           `json:"set"`
	}
	if json.Unmarshal(act.Params, &p) != nil || p.ID == 0 {
		return obBad("message_id required", act.Echo)
	}
	code := jsonEmojiID(p.EmojiID)
	if code == "" || len(code) > 64 {
		return obBad("emoji_id required", act.Echo)
	}
	s.ob.mu.Lock()
	m, ok := s.ob.cache[p.ID]
	s.ob.mu.Unlock()
	if !ok || time.Since(m.Created) > time.Hour {
		return obFail("message not in this account's bounded cache", act.Echo)
	}
	if m.Target.Group == 0 {
		return obFail("emoji like is a group message action", act.Echo)
	}
	if m.Result.Sequence == 0 {
		return obFail("emoji like requires sequence", act.Echo)
	}
	bot, ok := s.bot.(interface {
		SetGroupReaction(context.Context, uint32, uint32, string, bool) error
	})
	if !ok {
		return obUnsupported(act.Action, act.Echo)
	}
	add := p.Set == nil || *p.Set
	if err := bot.SetGroupReaction(ctx, m.Target.Group, m.Result.Sequence, code, add); err != nil {
		return obFail(err.Error(), act.Echo)
	}
	return obOK(nil, act.Echo)
}

func (s *Server) oneBotEssence(ctx context.Context, act obAction) obResponse {
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
	if m.Target.Group == 0 {
		return obFail("essence is a group message action", act.Echo)
	}
	if m.Result.Sequence == 0 || m.Result.Random == 0 {
		return obFail("essence requires sequence and random", act.Echo)
	}
	bot, ok := s.bot.(interface {
		SetEssenceMessage(context.Context, uint32, uint32, uint32, bool) error
	})
	if !ok {
		return obUnsupported(act.Action, act.Echo)
	}
	if err := bot.SetEssenceMessage(ctx, m.Target.Group, m.Result.Sequence, m.Result.Random, act.Action == "set_essence_msg"); err != nil {
		return obFail(err.Error(), act.Echo)
	}
	return obOK(nil, act.Echo)
}

func (s *Server) oneBotForwardSingle(ctx context.Context, act obAction) obResponse {
	var p struct {
		GroupID   uint64 `json:"group_id"`
		UserID    uint64 `json:"user_id"`
		MessageID int32  `json:"message_id"`
	}
	if json.Unmarshal(act.Params, &p) != nil || p.MessageID == 0 {
		return obBad("message_id required", act.Echo)
	}
	if act.Action == "forward_group_single_msg" && p.GroupID == 0 {
		return obBad("group_id required", act.Echo)
	}
	if act.Action == "forward_friend_single_msg" && p.UserID == 0 {
		return obBad("user_id required", act.Echo)
	}
	raw, _ := json.Marshal(map[string]any{
		"group_id": p.GroupID, "user_id": p.UserID,
		"messages": []any{map[string]any{"type": "node", "data": map[string]any{"id": p.MessageID}}},
	})
	return s.oneBotSendForward(ctx, obAction{Action: act.Action, Params: raw, Echo: act.Echo})
}

func (s *Server) oneBotKickMembers(ctx context.Context, act obAction) obResponse {
	var p struct {
		GroupID          uint64          `json:"group_id"`
		UserIDs          json.RawMessage `json:"user_ids"`
		UserID           json.RawMessage `json:"user_id"`
		RejectAddRequest bool            `json:"reject_add_request"`
	}
	if json.Unmarshal(act.Params, &p) != nil || p.GroupID == 0 {
		return obBad("group_id required", act.Echo)
	}
	raw := p.UserIDs
	if len(raw) == 0 {
		raw = p.UserID
	}
	ids, err := jsonUint32List(raw)
	if err != nil || len(ids) == 0 {
		return obBad("user_ids required", act.Echo)
	}
	if len(ids) > 50 {
		return obBad("at most 50 members", act.Echo)
	}
	mgr, ok := s.bot.(groupActions)
	if !ok {
		return obUnsupported(act.Action, act.Echo)
	}
	for _, uin := range ids {
		if uin == 0 {
			return obBad("invalid user_id", act.Echo)
		}
		if err := mgr.KickMember(ctx, uint32(p.GroupID), uin, p.RejectAddRequest); err != nil {
			return obFail(err.Error(), act.Echo)
		}
	}
	return obOK(nil, act.Echo)
}

func jsonUint32List(raw json.RawMessage) ([]uint32, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, fmt.Errorf("empty")
	}
	var numbers []uint64
	if json.Unmarshal(raw, &numbers) != nil {
		var one uint64
		if json.Unmarshal(raw, &one) != nil || one == 0 || one > math.MaxUint32 {
			return nil, fmt.Errorf("invalid")
		}
		return []uint32{uint32(one)}, nil
	}
	out := make([]uint32, 0, len(numbers))
	for _, n := range numbers {
		if n == 0 || n > math.MaxUint32 {
			return nil, fmt.Errorf("invalid")
		}
		out = append(out, uint32(n))
	}
	return out, nil
}

func (s *Server) oneBotSendForward(ctx context.Context, act obAction) obResponse {
	var p struct {
		GroupID  uint64          `json:"group_id"`
		UserID   uint64          `json:"user_id"`
		Messages json.RawMessage `json:"messages"`
	}
	if json.Unmarshal(act.Params, &p) != nil {
		return obBad("invalid forward parameters", act.Echo)
	}
	if act.Action == "send_group_forward_msg" && p.GroupID == 0 {
		return obBad("group_id required", act.Echo)
	}
	if act.Action == "send_private_forward_msg" && p.UserID == 0 {
		return obBad("user_id required", act.Echo)
	}
	if p.GroupID == 0 && p.UserID == 0 {
		return obBad("group_id or user_id required", act.Echo)
	}
	sender, ok := s.bot.(interface {
		SendForwardMessage(context.Context, protocol.SendTarget, []protocol.ForwardNode) (protocol.SendResult, error)
	})
	if !ok {
		return obUnsupported(act.Action, act.Echo)
	}
	nodes, err := s.expandForwardNodes(p.Messages)
	if err != nil {
		return obBad(err.Error(), act.Echo)
	}
	target := protocol.SendTarget{Group: uint32(p.GroupID), Uin: uint32(p.UserID)}
	target, err = s.prepareSendTarget(ctx, target)
	if err != nil {
		return obFail(err.Error(), act.Echo)
	}
	result, err := sender.SendForwardMessage(ctx, target, nodes)
	if err != nil {
		return obFail(err.Error(), act.Echo)
	}
	return obOK(map[string]any{"message_id": s.ob.rememberSend(ctx, target, result, "[聊天记录]")}, act.Echo)
}

func (s *Server) expandForwardNodes(raw json.RawMessage) ([]protocol.ForwardNode, error) {
	var segs []map[string]any
	if json.Unmarshal(raw, &segs) != nil || len(segs) == 0 {
		return nil, fmt.Errorf("messages must be a non-empty node array")
	}
	if len(segs) > 200 {
		return nil, fmt.Errorf("forward exceeds 200 nodes")
	}
	out := make([]protocol.ForwardNode, 0, len(segs))
	for _, seg := range segs {
		typ, _ := seg["type"].(string)
		if typ != "node" {
			return nil, fmt.Errorf("forward messages must be node segments")
		}
		data, _ := seg["data"].(map[string]any)
		if data == nil {
			return nil, fmt.Errorf("forward node is missing data")
		}
		if id := jsonInt32(data["id"]); id != 0 {
			s.ob.mu.Lock()
			m, ok := s.ob.cache[id]
			s.ob.mu.Unlock()
			if !ok || time.Since(m.Created) > time.Hour {
				return nil, fmt.Errorf("forward node id %d is not in this account's cache", id)
			}
			n := protocol.ForwardNode{}
			if m.Reply != nil {
				n.SenderUin, n.SenderUid = m.Reply.SenderUin, m.Reply.SenderUid
				n.Time, n.Sequence, n.Elements = m.Reply.Timestamp, m.Reply.Sequence, m.Reply.Elements
			}
			if sender, _ := m.Data["sender"].(map[string]any); sender != nil {
				if nick, _ := sender["nickname"].(string); nick != "" {
					n.SenderName = nick
				}
				if n.SenderUin == 0 {
					n.SenderUin = jsonU32(sender["user_id"])
				}
			}
			if n.SenderUin == 0 {
				n.SenderUin = jsonU32(m.Data["user_id"])
			}
			if len(n.Elements) == 0 {
				if cq, _ := m.Data["raw_message"].(string); cq != "" {
					elems, err := cqToForwardElems(cq)
					if err != nil {
						return nil, err
					}
					n.Elements = elems
				} else {
					n.Elements = [][]byte{protocol.TextElem("")}
				}
			}
			out = append(out, n)
			continue
		}
		content, _ := json.Marshal(data["content"])
		cq, err := validatedOBMessage(content, false, true)
		if err != nil {
			return nil, err
		}
		elems, err := cqToForwardElems(cq)
		if err != nil {
			return nil, err
		}
		uin := jsonU32(data["user_id"])
		if uin == 0 {
			uin = jsonU32(data["uin"])
		}
		if uin == 0 {
			return nil, fmt.Errorf("forward node needs user_id or id")
		}
		nick, _ := data["nickname"].(string)
		out = append(out, protocol.ForwardNode{
			SenderUin: uin, SenderName: nick, Time: jsonU32(data["time"]), Elements: elems,
		})
	}
	return out, nil
}

func cqToForwardElems(cq string) ([][]byte, error) {
	var elems [][]byte
	for _, seg := range cqSegments(cq) {
		typ := seg["type"].(string)
		d := seg["data"].(map[string]string)
		switch typ {
		case "text":
			if d["text"] != "" {
				elems = append(elems, protocol.TextElem(d["text"]))
			}
		case "at":
			at := protocol.SendAt{}
			if d["qq"] != "all" {
				v, err := strconv.ParseUint(d["qq"], 10, 32)
				if err != nil {
					return nil, fmt.Errorf("invalid mention")
				}
				at.Uin = uint32(v)
			}
			elems = append(elems, protocol.AtElem(at))
		case "face":
			v, err := strconv.ParseUint(d["id"], 10, 32)
			if err != nil {
				return nil, fmt.Errorf("invalid face")
			}
			elems = append(elems, protocol.Marshal(protocol.BytesField(2, protocol.Marshal(protocol.VarintField(1, v)))))
		case "image", "record", "video", "file", "reply":
			return nil, fmt.Errorf("forward nodes only support text, at and face")
		default:
			return nil, fmt.Errorf("unsupported forward segment %s", typ)
		}
	}
	if len(elems) == 0 {
		elems = [][]byte{protocol.TextElem("")}
	}
	return elems, nil
}

func jsonU32(v any) uint32 {
	switch t := v.(type) {
	case float64:
		if t <= 0 || t > float64(^uint32(0)) {
			return 0
		}
		return uint32(t)
	case string:
		n, err := strconv.ParseUint(t, 10, 32)
		if err != nil {
			return 0
		}
		return uint32(n)
	case json.Number:
		n, err := t.Int64()
		if err != nil || n <= 0 || n > int64(^uint32(0)) {
			return 0
		}
		return uint32(n)
	}
	return 0
}

func jsonInt32(v any) int32 {
	n := jsonU32(v)
	if n > uint32(^uint32(0)>>1) {
		return 0
	}
	return int32(n)
}
