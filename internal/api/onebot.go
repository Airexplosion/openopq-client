package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"openopq/internal/buildinfo"
	"openopq/internal/mediaconv"
	"openopq/internal/protocol"
)

// OneBot v11 adapter: the same decoded messages the OPQ `/ws` serves are offered in the OneBot
// v11 shape as well — message events as CQ-code strings, and the common actions (send_group_msg,
// send_private_msg, send_msg, get_login_info, get_status, get_version_info). A forward websocket
// (/onebot/v11/ws) pushes events and takes action requests; an HTTP endpoint takes actions too.
// Which protocols are mounted is the server's Protocol setting.

// --- CQ codes ---

func cqEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "[", "&#91;", "]", "&#93;")
	return r.Replace(s)
}

func cqEscapeParam(s string) string {
	r := strings.NewReplacer("&", "&amp;", "[", "&#91;", "]", "&#93;", ",", "&#44;")
	return r.Replace(s)
}

func cqUnescape(s string) string {
	r := strings.NewReplacer("&#44;", ",", "&#91;", "[", "&#93;", "]", "&amp;", "&")
	return r.Replace(s)
}

// cqMessage uses the ordered native segments when available. Legacy sources
// with only separate text/mentions cannot reconstruct their original positions.
func cqMessage(m *protocol.IncomingMessage) string {
	var b strings.Builder
	if len(m.Segments) > 0 {
		for _, seg := range m.Segments {
			if seg.Type == "text" {
				b.WriteString(cqEscape(seg.Data["text"]))
				continue
			}
			b.WriteString("[CQ:" + seg.Type)
			keys := []string{}
			for k := range seg.Data {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				b.WriteString("," + k + "=" + cqEscapeParam(seg.Data[k]))
			}
			b.WriteString("]")
		}
		return b.String()
	}
	for _, at := range m.Ats {
		fmt.Fprintf(&b, "[CQ:at,qq=%d]", at.Uin)
	}
	b.WriteString(cqEscape(m.Content))
	for _, img := range m.Images {
		if img.URL != "" {
			fmt.Fprintf(&b, "[CQ:image,file=%s,url=%s]", cqEscapeParam(img.URL), cqEscapeParam(img.URL))
		}
	}
	return b.String()
}

// parseCQText extracts the plain text and the @mentions from a CQ-code string. Segments this
// build cannot send (images, etc.) are skipped; sending them needs the upload protocol.
func parseCQText(msg string) (text string, ats []protocol.SendAt) {
	var out strings.Builder
	for len(msg) > 0 {
		open := strings.Index(msg, "[CQ:")
		if open < 0 {
			out.WriteString(cqUnescape(msg))
			break
		}
		out.WriteString(cqUnescape(msg[:open]))
		close := strings.Index(msg[open:], "]")
		if close < 0 {
			out.WriteString(cqUnescape(msg[open:]))
			break
		}
		seg := msg[open+4 : open+close] // after "[CQ:" up to "]"
		msg = msg[open+close+1:]
		parts := strings.Split(seg, ",")
		switch parts[0] {
		case "at":
			for _, p := range parts[1:] {
				if v, ok := strings.CutPrefix(p, "qq="); ok {
					if v == "all" {
						ats = append(ats, protocol.SendAt{Uin: 0})
						continue
					}
					if uin, err := strconv.ParseUint(v, 10, 32); err == nil {
						ats = append(ats, protocol.SendAt{Uin: uint32(uin)})
					}
				}
			}
		}
	}
	return out.String(), ats
}

// --- events ---

func onebotEventJSON(ev protocol.MessageEvent) []byte {
	if ev.Notice != nil {
		return onebotNoticeJSON(ev)
	}
	if ev.Msg == nil {
		return nil
	}
	m := ev.Msg
	cq := cqMessage(m)
	sender := map[string]any{"user_id": m.SenderUin, "nickname": m.SenderNick, "sex": "unknown", "age": 0}
	e := map[string]any{
		"time": int64(m.MsgTime), "self_id": ev.SelfUin, "post_type": "message",
		"message_id": int32(m.MsgSeq), "message_seq": m.MsgSeq, "user_id": m.SenderUin,
		"message": cq, "raw_message": cq, "font": 0, "sender": sender,
	}
	if m.MsgTime == 0 {
		e["time"] = time.Now().Unix()
	}
	if ev.SelfUin != 0 && m.SenderUin == ev.SelfUin {
		e["post_type"] = "message_sent"
		if !m.IsGroup && m.ToUin != 0 && m.ToUin != ev.SelfUin {
			e["target_id"] = m.ToUin
		}
	}
	if m.IsGroup {
		e["message_type"], e["sub_type"], e["group_id"] = "group", "normal", m.GroupUin
		e["group_name"], e["anonymous"] = m.GroupName, nil
		sender["card"], sender["role"] = m.GroupCard, "member"
	} else {
		e["message_type"], e["sub_type"] = "private", "friend"
		if m.IsTemporary {
			e["sub_type"] = "group"
			if m.TempGroupUin != 0 {
				sender["group_id"] = m.TempGroupUin
			}
		}
	}
	out, _ := json.Marshal(e)
	return out
}

// onebotNoticeJSON formats a notice as a standard OneBot v11 notice event, or nil for a kind it
// does not map. People are named by uin (user_id/operator_id); a uin the bot has not learned
// comes through as 0.
func onebotNoticeJSON(ev protocol.MessageEvent) []byte {
	n := ev.Notice
	e := map[string]any{"post_type": "notice", "self_id": ev.SelfUin, "time": int64(n.MsgTime)}
	if n.MsgTime == 0 {
		e["time"] = time.Now().Unix()
	}
	switch n.Kind {
	case protocol.NoticeFriendRequest, protocol.NoticeGroupRequest:
		if n.Flag == "" || n.UserUin == 0 {
			return nil
		}
		e["post_type"] = "request"
		e["request_type"] = "friend"
		e["user_id"], e["comment"], e["flag"] = n.UserUin, n.Comment, n.Flag
		if n.Kind == protocol.NoticeGroupRequest {
			e["request_type"] = "group"
			e["sub_type"], e["group_id"] = n.SubType, n.GroupUin
		}
	case protocol.NoticeGroupJoin:
		e["notice_type"], e["sub_type"] = "group_increase", "approve"
		if n.IncreaseType&^0x80 == 3 {
			e["sub_type"] = "invite"
		}
		e["group_id"], e["operator_id"], e["user_id"] = n.GroupUin, n.OperatorUin, n.UserUin
	case protocol.NoticeGroupLeave:
		sub := "leave"
		switch n.ExitType {
		case 0, 130:
		case 129:
			sub = "disband"
		default:
			sub = "kick"
			if n.UserUin == ev.SelfUin {
				sub = "kick_me"
			}
		}
		e["notice_type"], e["sub_type"] = "group_decrease", sub
		e["group_id"], e["operator_id"], e["user_id"] = n.GroupUin, n.OperatorUin, n.UserUin
	case protocol.NoticeGroupAdmin:
		sub := "unset"
		if n.Enable {
			sub = "set"
		}
		e["notice_type"], e["sub_type"] = "group_admin", sub
		e["group_id"], e["user_id"] = n.GroupUin, n.UserUin
	case protocol.NoticeGroupRecall:
		e["notice_type"] = "group_recall"
		e["group_id"], e["user_id"], e["operator_id"] = n.GroupUin, n.UserUin, n.OperatorUin
		e["message_id"] = int32(n.MsgSeq)
	case protocol.NoticeGroupMute:
		sub := "ban"
		if n.Duration == 0 {
			sub = "lift_ban"
		}
		e["notice_type"], e["sub_type"] = "group_ban", sub
		e["group_id"], e["operator_id"], e["user_id"] = n.GroupUin, n.OperatorUin, n.UserUin
		e["duration"] = n.Duration
	case protocol.NoticeGroupPoke:
		e["notice_type"], e["sub_type"] = "notify", "poke"
		e["group_id"], e["user_id"], e["target_id"] = n.GroupUin, n.UserUin, n.Target
		e["action"], e["suffix"], e["action_img_url"] = n.Action, n.Suffix, n.ActionImageURL
	case protocol.NoticeFriendRecall:
		e["notice_type"] = "friend_recall"
		e["user_id"] = n.UserUin
		e["message_id"] = int32(n.MsgSeq)
	case protocol.NoticeFriendPoke:
		e["notice_type"], e["sub_type"] = "notify", "poke"
		peer := n.UserUin
		if peer == ev.SelfUin {
			peer = n.Target
		}
		e["user_id"], e["sender_id"], e["target_id"] = peer, n.UserUin, n.Target
		e["action"], e["suffix"], e["action_img_url"] = n.Action, n.Suffix, n.ActionImageURL
	default:
		return nil
	}
	out, _ := json.Marshal(e)
	return out
}

// --- actions ---

type obAction struct {
	Action string          `json:"action"`
	Params json.RawMessage `json:"params"`
	Echo   json.RawMessage `json:"echo"`
}

type obResponse struct {
	Status      string          `json:"status"`
	Retcode     int             `json:"retcode"`
	Data        any             `json:"data"`
	Message     string          `json:"wording,omitempty"`
	Echo        json.RawMessage `json:"echo,omitempty"`
	Unavailable bool            `json:"-"` // Preserve v12 error classification without leaking adapter codes.
}

func obOK(data any, echo json.RawMessage) obResponse {
	return obResponse{Status: "ok", Retcode: 0, Data: data, Echo: echo}
}
func obFail(msg string, echo json.RawMessage) obResponse {
	return obResponse{Status: "failed", Retcode: 100, Data: nil, Message: msg, Echo: echo}
}

// obMessageParam is the "message" field of a send action: OneBot allows a CQ string or an array
// of segments. We render both to a CQ string and reuse parseCQText.
func obMessageToCQ(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var segs []struct {
		Type string                 `json:"type"`
		Data map[string]interface{} `json:"data"`
	}
	if json.Unmarshal(raw, &segs) != nil {
		return ""
	}
	var b strings.Builder
	for _, seg := range segs {
		switch seg.Type {
		case "text":
			if t, ok := seg.Data["text"].(string); ok {
				b.WriteString(cqEscape(t))
			}
		case "at":
			if q, ok := seg.Data["qq"]; ok {
				fmt.Fprintf(&b, "[CQ:at,qq=%v]", q)
			}
		case "image":
			src, _ := seg.Data["url"].(string)
			if src == "" {
				src, _ = seg.Data["file"].(string)
			}
			if src != "" {
				fmt.Fprintf(&b, "[CQ:image,file=%s]", cqEscapeParam(src))
			}
		case "record":
			src, _ := seg.Data["url"].(string)
			if src == "" {
				src, _ = seg.Data["file"].(string)
			}
			if src != "" {
				fmt.Fprintf(&b, "[CQ:record,file=%s]", cqEscapeParam(src))
			}
		}
	}
	return b.String()
}

// imageSender / voiceSender are the rich-media send surfaces: the client bot provides them; the
// stub does not.
type imageSender interface {
	UploadAndSendImage(ctx context.Context, target protocol.SendTarget, data []byte, caption string) (protocol.SendResult, error)
}

type voiceSender interface {
	UploadAndSendVoice(ctx context.Context, target protocol.SendTarget, data []byte, duration uint32) (protocol.SendResult, error)
}

// parseCQRecords returns the source of each [CQ:record] (voice) segment in a CQ string.
func parseCQRecords(msg string) []string {
	var out []string
	for len(msg) > 0 {
		open := strings.Index(msg, "[CQ:record")
		if open < 0 {
			break
		}
		closeIdx := strings.Index(msg[open:], "]")
		if closeIdx < 0 {
			break
		}
		seg := msg[open+4 : open+closeIdx]
		msg = msg[open+closeIdx+1:]
		var file, url string
		for _, p := range strings.Split(seg, ",")[1:] {
			if v, ok := strings.CutPrefix(p, "file="); ok {
				file = cqUnescape(v)
			} else if v, ok := strings.CutPrefix(p, "url="); ok {
				url = cqUnescape(v)
			}
		}
		if url != "" {
			out = append(out, url)
		} else if file != "" {
			out = append(out, file)
		}
	}
	return out
}

// parseCQImages returns the source (file/url/base64) of each [CQ:image] segment in a CQ string.
func parseCQImages(msg string) []string {
	var out []string
	for len(msg) > 0 {
		open := strings.Index(msg, "[CQ:image")
		if open < 0 {
			break
		}
		close := strings.Index(msg[open:], "]")
		if close < 0 {
			break
		}
		seg := msg[open+4 : open+close] // "image,file=...,url=..."
		msg = msg[open+close+1:]
		var file, url string
		for _, p := range strings.Split(seg, ",")[1:] {
			if v, ok := strings.CutPrefix(p, "file="); ok {
				file = cqUnescape(v)
			} else if v, ok := strings.CutPrefix(p, "url="); ok {
				url = cqUnescape(v)
			}
		}
		if url != "" {
			out = append(out, url)
		} else if file != "" {
			out = append(out, file)
		}
	}
	return out
}

// fetchImageData resolves a CQ image source to bytes: a data URI / base64:// value, an http(s)
// URL, a file:// URL, or a local path.
func fetchMediaData(ctx context.Context, src string) ([]byte, error) {
	switch {
	case strings.HasPrefix(src, "base64://"):
		return base64.StdEncoding.DecodeString(src[len("base64://"):])
	case strings.HasPrefix(src, "http://"), strings.HasPrefix(src, "https://"):
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
		if err != nil {
			return nil, err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("http %d", resp.StatusCode)
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, (30<<20)+1))
		if err != nil {
			return nil, err
		}
		if len(data) > 30<<20 {
			return nil, fmt.Errorf("media exceeds 30 MiB")
		}
		return data, nil
	case strings.HasPrefix(src, "file://"):
		return os.ReadFile(strings.TrimPrefix(src, "file://"))
	default:
		return os.ReadFile(src)
	}
}

// groupActions is the group/friend management surface the action endpoints map onto. The client
// bot provides it; a bot that does not (the stub) makes those actions report "not supported".
type groupActions interface {
	RecallGroupMessage(ctx context.Context, groupUin, seq uint32) error
	MuteMember(ctx context.Context, groupUin, uin, duration uint32) error
	MuteGroup(ctx context.Context, groupUin uint32, mute bool) error
	KickMember(ctx context.Context, groupUin, uin uint32, rejectAddRequest bool) error
	SetAdmin(ctx context.Context, groupUin, uin uint32, admin bool) error
	SetMemberCard(ctx context.Context, groupUin, uin uint32, name string) error
	SetGroupName(ctx context.Context, groupUin uint32, name string) error
	LeaveGroup(ctx context.Context, groupUin uint32) error
	SetSpecialTitle(ctx context.Context, groupUin, uin uint32, title string) error
	PokeMember(ctx context.Context, groupUin, uin uint32) error
	PokeFriend(ctx context.Context, uin uint32) error
}

// handleOneBotAction runs one OneBot action and returns its response.
func (s *Server) dispatchOneBotAction(ctx context.Context, act obAction) obResponse {
	// Group/friend management actions share a parameter shape; decode once and dispatch.
	switch act.Action {
	case "set_group_ban", "set_group_whole_ban", "set_group_kick",
		"set_group_admin", "set_group_card", "set_group_name", "set_group_leave",
		"set_group_special_title", "group_poke", "send_group_poke", "send_poke", "friend_poke":
		return s.handleOneBotManage(ctx, act)
	}
	switch act.Action {
	case "send_group_msg", "send_private_msg", "send_msg":
		var p struct {
			MessageType string          `json:"message_type"`
			GroupID     uint64          `json:"group_id"`
			UserID      uint64          `json:"user_id"`
			Message     json.RawMessage `json:"message"`
			AutoEscape  bool            `json:"auto_escape"`
		}
		if err := json.Unmarshal(act.Params, &p); err != nil {
			return obBad("invalid send parameters", act.Echo)
		}
		if err := validateSendTarget(act.Action, p.MessageType, p.UserID, p.GroupID); err != nil {
			return obBad(err.Error(), act.Echo)
		}
		sender, ok := s.bot.(messageSender)
		if !ok {
			return obUnsupported("send", act.Echo)
		}
		richSender, nativeRich := s.bot.(interface {
			SendRichMessage(context.Context, protocol.SendTarget, []protocol.SendSegment) (protocol.SendResult, error)
		})
		cq, err := validatedOBMessage(p.Message, p.AutoEscape, nativeRich)
		if err != nil {
			return obBad(err.Error(), act.Echo)
		}
		text, ats := parseCQText(cq)
		var target protocol.SendTarget
		isGroup := act.Action == "send_group_msg" || p.MessageType == "group" || (act.Action == "send_msg" && p.MessageType == "" && p.GroupID != 0)
		if isGroup {
			target.Group = uint32(p.GroupID)
		} else {
			target.Uin = uint32(p.UserID)
			target.TempGroup = uint32(p.GroupID)
		}
		target, err = s.prepareSendTarget(ctx, target)
		if err != nil {
			return obFail(err.Error(), act.Echo)
		}

		if nativeRich {
			segments := []protocol.SendSegment{}
			total := 0
			for _, seg := range cqSegments(cq) {
				typ := seg["type"].(string)
				d := seg["data"].(map[string]string)
				part := protocol.SendSegment{Type: typ}
				switch typ {
				case "reply":
					id, _ := strconv.ParseInt(d["id"], 10, 32)
					part.Reply, err = s.cachedReply(int32(id), target)
					if err != nil {
						return obBad(err.Error(), act.Echo)
					}
				case "text":
					part.Text = d["text"]
				case "at":
					if d["qq"] != "all" {
						v, _ := strconv.ParseUint(d["qq"], 10, 32)
						part.At.Uin = uint32(v)
					}
				case "face":
					v, _ := strconv.ParseUint(d["id"], 10, 32)
					part.Face = uint32(v)
				case "image", "record":
					src := d["file"]
					if src == "" {
						src = d["url"]
					}
					data, e := fetchMediaData(ctx, src)
					if e != nil {
						return obFail(e.Error(), act.Echo)
					}
					if typ == "record" {
						data, duration, e := mediaconv.Encode(ctx, data)
						if e != nil {
							return obFail("record: "+e.Error(), act.Echo)
						}
						part.Bytes, part.Duration = data, duration
					} else {
						part.Bytes = data
					}
					total += len(part.Bytes)
					if total > 60<<20 {
						return obBad("message media exceed 60 MiB", act.Echo)
					}
				}
				segments = append(segments, part)
			}
			result, e := richSender.SendRichMessage(ctx, target, segments)
			if e != nil {
				return obFail(e.Error(), act.Echo)
			}
			return obOK(map[string]any{"message_id": s.ob.rememberSend(ctx, target, result, cq)}, act.Echo)
		}
		// Compatibility backend: validation above only permits one image here.
		if images := parseCQImages(cq); len(images) > 0 {
			imgSender, ok := s.bot.(imageSender)
			if !ok {
				return obFail("image send not supported", act.Echo)
			}
			data, err := fetchMediaData(ctx, images[0])
			if err != nil {
				return obFail("image: "+err.Error(), act.Echo)
			}
			res, err := imgSender.UploadAndSendImage(ctx, target, data, text)
			if err != nil {
				return obFail(err.Error(), act.Echo)
			}
			return obOK(map[string]any{"message_id": s.ob.rememberSend(ctx, target, res, cq)}, act.Echo)
		}
		// Convert ordinary audio on demand, preserving its measured duration.
		if records := parseCQRecords(cq); len(records) > 0 {
			vSender, ok := s.bot.(voiceSender)
			if !ok {
				return obFail("voice send not supported", act.Echo)
			}
			data, err := fetchMediaData(ctx, records[0])
			if err != nil {
				return obFail("record: "+err.Error(), act.Echo)
			}
			data, duration, err := mediaconv.Encode(ctx, data)
			if err != nil {
				return obFail("record: "+err.Error(), act.Echo)
			}
			res, err := vSender.UploadAndSendVoice(ctx, target, data, duration)
			if err != nil {
				return obFail(err.Error(), act.Echo)
			}
			return obOK(map[string]any{"message_id": s.ob.rememberSend(ctx, target, res, cq)}, act.Echo)
		}
		res, err := sender.SendMessage(ctx, target, text, ats)
		if err != nil {
			return obFail(err.Error(), act.Echo)
		}
		return obOK(map[string]any{"message_id": s.ob.rememberSend(ctx, target, res, cq)}, act.Echo)

	case "get_login_info":
		self, err := s.bot.Self(ctx)
		if err != nil {
			return obFail(err.Error(), act.Echo)
		}
		return obOK(map[string]any{"user_id": self.Uin, "nickname": self.Nickname}, act.Echo)

	case "get_group_list":
		groups, err := s.bot.Groups(ctx)
		if err != nil {
			return obFail(err.Error(), act.Echo)
		}
		out := make([]map[string]any, 0, len(groups))
		for _, group := range groups {
			out = append(out, oneBotGroupInfo(group))
		}
		return obOK(out, act.Echo)

	case "get_status":
		return obOK(s.ob.statusData(ctx), act.Echo)

	case "get_version_info":
		return obOK(map[string]any{
			"app_name": "openopq", "app_version": buildinfo.Version, "protocol_version": "v11",
		}, act.Echo)

	default:
		return s.extraOneBotAction(ctx, act)
	}
}

// handleOneBotManage runs the group/friend management actions. They share a parameter shape, so
// it decodes once and dispatches. A member is named by user_id (uin); the bot resolves the uid
// the NT request needs from what it has seen, so an action on someone it has never observed
// reports that it cannot.
func (s *Server) handleOneBotManage(ctx context.Context, act obAction) obResponse {
	var p struct {
		GroupID          uint64 `json:"group_id"`
		UserID           uint64 `json:"user_id"`
		MessageID        int32  `json:"message_id"`
		Duration         *int64 `json:"duration"`
		Enable           *bool  `json:"enable"`
		RejectAddRequest bool   `json:"reject_add_request"`
		Card             string `json:"card"`
		GroupName        string `json:"group_name"`
		SpecialTitle     string `json:"special_title"`
	}
	if err := json.Unmarshal(act.Params, &p); err != nil {
		return obBad("invalid management parameters", act.Echo)
	}
	duration := uint32(1800)
	if act.Action == "set_group_special_title" {
		if p.Duration != nil && *p.Duration != -1 {
			return obUnsupported("non-permanent special title", act.Echo)
		}
	} else if p.Duration != nil {
		if *p.Duration < 0 || *p.Duration > 30*24*3600 {
			return obBad("invalid duration", act.Echo)
		}
		duration = uint32(*p.Duration)
	}
	if err := validateManage(act.Action, p.GroupID, p.UserID, duration); err != nil {
		return obBad(err.Error(), act.Echo)
	}
	mgr, ok := s.bot.(groupActions)
	if !ok {
		return s.backendOneBotAction(ctx, act)
	}
	enabled := p.Enable == nil || *p.Enable // most "enable" actions default to true when omitted

	var err error
	switch act.Action {
	case "delete_msg":
		return s.deleteOneBotMessage(ctx, p.MessageID, act.Echo)
	case "set_group_ban":
		err = mgr.MuteMember(ctx, uint32(p.GroupID), uint32(p.UserID), duration)
	case "set_group_whole_ban":
		err = mgr.MuteGroup(ctx, uint32(p.GroupID), enabled)
	case "set_group_kick":
		err = mgr.KickMember(ctx, uint32(p.GroupID), uint32(p.UserID), p.RejectAddRequest)
	case "set_group_admin":
		err = mgr.SetAdmin(ctx, uint32(p.GroupID), uint32(p.UserID), enabled)
	case "set_group_card":
		err = mgr.SetMemberCard(ctx, uint32(p.GroupID), uint32(p.UserID), p.Card)
	case "set_group_name":
		err = mgr.SetGroupName(ctx, uint32(p.GroupID), p.GroupName)
	case "set_group_leave":
		var flags struct {
			IsDismiss bool `json:"is_dismiss"`
		}
		_ = json.Unmarshal(act.Params, &flags)
		if flags.IsDismiss {
			return obUnsupported("group dismissal", act.Echo)
		}
		err = mgr.LeaveGroup(ctx, uint32(p.GroupID))
	case "set_group_special_title":
		err = mgr.SetSpecialTitle(ctx, uint32(p.GroupID), uint32(p.UserID), p.SpecialTitle)
	case "group_poke", "send_group_poke":
		err = mgr.PokeMember(ctx, uint32(p.GroupID), uint32(p.UserID))
	case "send_poke":
		if p.GroupID != 0 {
			err = mgr.PokeMember(ctx, uint32(p.GroupID), uint32(p.UserID))
		} else {
			err = mgr.PokeFriend(ctx, uint32(p.UserID))
		}
	case "friend_poke":
		err = mgr.PokeFriend(ctx, uint32(p.UserID))
	default:
		return obFail("unsupported action: "+act.Action, act.Echo)
	}
	if err != nil {
		return obFail(err.Error(), act.Echo)
	}
	return obOK(nil, act.Echo)
}

// Standard transports share the same dispatcher and per-account runtime.
func (s *Server) handleOneBotHTTP(w http.ResponseWriter, r *http.Request) { s.serveOneBotHTTP(w, r) }
func (s *Server) handleOneBotWS(w http.ResponseWriter, r *http.Request)   { s.serveOneBotWS(w, r) }
