package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"

	"openopq/internal/bot"
	"openopq/internal/protocol"
)

// NewOneBotQueryBackend adapts an already authenticated exchange. It performs no
// login, registration, heartbeat or signer setup and does not own the exchange.
// Used by the upgrade gateway to keep existing QQ connections intact.
func NewOneBotQueryBackend(exchange Exchange) *Bot { return &Bot{exchange: exchange} }

func (b *Bot) SupportedOneBotActions() []string {
	return []string{"set_friend_add_request", "set_group_add_request", "get_forward_msg", "get_image", "get_record", "get_group_system_msg", "clean_cache", "get_friend_list", "get_group_member_list", "get_group_member_info", "get_stranger_info", "send_like", "get_cookies", "get_csrf_token", "get_credentials"}
}

// Field identifiers follow the published NT protobuf schemas, documented in
// docs/onebot-v11.md. This codec uses our own existing protobuf reader/writer.
func obFields(data []byte) []protocol.Field { v, _ := protocol.EveryField(data); return v }
func obBytes(f []protocol.Field, k int) []byte {
	v, ok := protocol.FirstField(f, k)
	if !ok || !v.IsData {
		return nil
	}
	return v.Data
}
func obNumber(f []protocol.Field, k int) uint32 {
	v, _ := protocol.FirstField(f, k)
	return uint32(v.Number)
}
func obString(f []protocol.Field, k int) string { return string(obBytes(f, k)) }
func (b *Bot) obQuery(ctx context.Context, command string, body []byte, uin bool) ([]protocol.Field, error) {
	cmd, err := protocol.ParseOIDBCommand(command)
	if err != nil {
		return nil, err
	}
	var reserved uint64
	if uin {
		reserved = 1
	}
	ex, err := b.service()
	if err != nil {
		return nil, err
	}
	answer, err := ex.Exchange(ctx, command, protocol.BuildOIDB(protocol.OIDBRequest{Command: cmd, Body: body, Reserved: reserved}))
	if err != nil {
		return nil, err
	}
	reply, err := protocol.UnwrapOIDB(answer)
	if err != nil {
		return nil, err
	}
	if reply.Command != cmd {
		return nil, fmt.Errorf("mismatched query response")
	}
	if reply.Failed() {
		return nil, fmt.Errorf("QQ refused query: %d %s", reply.ErrorCode, reply.ErrorMessage)
	}
	return protocol.EveryField(reply.Body)
}
func (b *Bot) oneBotFriends(ctx context.Context) ([]map[string]any, error) {
	out := []map[string]any{}
	seen := map[uint32]bool{}
	var token uint32
	for page := 0; page < 100; page++ {
		body := []protocol.Field{protocol.VarintField(2, 300), protocol.VarintField(4, 0), protocol.VarintField(6, 1), protocol.VarintField(7, 2147483647), protocol.VarintField(10003, 4051)}
		if token != 0 {
			body = append(body, protocol.BytesField(5, protocol.Marshal(protocol.VarintField(1, uint64(token)))))
		}
		for _, q := range []struct {
			kind  uint64
			codes []uint64
		}{{1, []uint64{103, 102, 20002, 27394}}, {4, []uint64{100, 101, 102}}} {
			var f []protocol.Field
			for _, n := range q.codes {
				f = append(f, protocol.VarintField(1, n))
			}
			body = append(body, protocol.BytesField(10001, protocol.Marshal(protocol.VarintField(1, q.kind), protocol.BytesField(2, protocol.Marshal(f...)))))
		}
		for _, n := range []uint64{13578, 13579, 13573, 13572, 13568} {
			body = append(body, protocol.VarintField(10002, n))
		}
		fields, err := b.obQuery(ctx, protocol.FetchFriendsCommand, protocol.Marshal(body...), false)
		if err != nil {
			return nil, err
		}
		for _, f := range fields {
			if f.Tag != 101 || !f.IsData {
				continue
			}
			v := obFields(f.Data)
			uin := obNumber(v, 3)
			uid := obString(v, 1)
			if uin == 0 || uid == "" {
				return nil, fmt.Errorf("invalid friend identity in response")
			}
			props := map[uint32]string{}
			for _, a := range v {
				if a.Tag != 10001 || !a.IsData {
					continue
				}
				af := obFields(a.Data)
				if obNumber(af, 1) != 1 {
					continue
				}
				for _, p := range obFields(obBytes(af, 2)) {
					if p.Tag == 2 && p.IsData {
						pf := obFields(p.Data)
						props[obNumber(pf, 1)] = obString(pf, 2)
					}
				}
			}
			b.remember(uin, uid, props[20002])
			out = append(out, map[string]any{"user_id": uin, "nickname": props[20002], "remark": props[103]})
		}
		token = obNumber(obFields(obBytes(fields, 2)), 1)
		if token == 0 {
			return out, nil
		}
		if seen[token] {
			return nil, fmt.Errorf("friend pagination repeated token")
		}
		seen[token] = true
	}
	return nil, fmt.Errorf("friend pagination limit reached")
}
func (b *Bot) oneBotMembers(ctx context.Context, group uint32) ([]map[string]any, error) {
	out := []map[string]any{}
	seen := map[string]bool{}
	token := ""
	for page := 0; page < 200; page++ {
		var selectFields []protocol.Field
		for _, k := range []int{10, 11, 12, 17, 100, 101, 102, 107} {
			selectFields = append(selectFields, protocol.VarintField(k, 1))
		}
		body := protocol.Marshal(protocol.VarintField(1, uint64(group)), protocol.VarintField(2, 5), protocol.VarintField(3, 2), protocol.BytesField(4, protocol.Marshal(selectFields...)), protocol.StringField(15, token))
		fields, err := b.obQuery(ctx, protocol.OidbCommand(0xfe7, 3), body, false)
		if err != nil {
			return nil, err
		}
		if g := obNumber(fields, 1); g != group {
			return nil, fmt.Errorf("group member response identity mismatch")
		}
		for _, f := range fields {
			if f.Tag != 2 || !f.IsData {
				continue
			}
			v := obFields(f.Data)
			identity := obFields(obBytes(v, 1))
			uin, uid := obNumber(identity, 4), obString(identity, 2)
			if uin == 0 || uid == "" {
				return nil, fmt.Errorf("invalid group member identity")
			}
			nick := obString(v, 10)
			b.remember(uin, uid, nick)
			role := "member"
			switch obNumber(v, 107) {
			case 1:
				role = "owner"
			case 2:
				role = "admin"
			}
			out = append(out, map[string]any{"group_id": group, "user_id": uin, "nickname": nick, "card": obString(obFields(obBytes(v, 11)), 2), "sex": "unknown", "age": 0, "area": "", "join_time": obNumber(v, 100), "last_sent_time": obNumber(v, 101), "shut_up_timestamp": obNumber(v, 102), "level": strconv.FormatUint(uint64(obNumber(obFields(obBytes(v, 12)), 2)), 10), "role": role, "unfriendly": false, "title": obString(v, 17), "title_expire_time": 0, "card_changeable": true})
		}
		token = obString(fields, 15)
		if token == "" {
			return out, nil
		}
		if seen[token] {
			return nil, fmt.Errorf("member pagination repeated token")
		}
		seen[token] = true
	}
	return nil, fmt.Errorf("group member pagination limit reached")
}
func (b *Bot) OneBotAction(ctx context.Context, action string, raw json.RawMessage) (any, error) {
	var p struct {
		UserID  uint32  `json:"user_id"`
		GroupID uint32  `json:"group_id"`
		Times   *uint32 `json:"times"`
		Domain  string  `json:"domain"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("invalid action parameters: %w", err)
	}
	switch action {
	case "set_friend_add_request", "set_group_add_request", "get_group_system_msg":
		return b.requestAction(ctx, action, raw)
	case "get_forward_msg":
		return b.forwardAction(ctx, raw)
	case "clean_cache":
		return nil, b.cleanMedia()
	case "get_image", "get_record":
		return b.mediaAction(ctx, action, raw)
	case "get_friend_list":
		return b.oneBotFriends(ctx)
	case "get_group_member_list", "get_group_member_info":
		if p.GroupID == 0 {
			return nil, fmt.Errorf("group_id required")
		}
		members, e := b.oneBotMembers(ctx, p.GroupID)
		if e != nil {
			return nil, e
		}
		if action == "get_group_member_list" {
			return members, nil
		}
		if p.UserID == 0 {
			return nil, fmt.Errorf("user_id required")
		}
		for _, m := range members {
			if m["user_id"] == p.UserID {
				return m, nil
			}
		}
		return nil, fmt.Errorf("member not found")
	case "get_stranger_info":
		if p.UserID == 0 {
			return nil, fmt.Errorf("user_id required")
		}
		f := []protocol.Field{protocol.VarintField(1, uint64(p.UserID)), protocol.VarintField(2, 0)}
		for _, k := range []uint64{20002, 20009, 20037, 103, 102} {
			f = append(f, protocol.BytesField(3, protocol.Marshal(protocol.VarintField(1, k))))
		}
		reply, e := b.obQuery(ctx, protocol.OidbCommand(0xfe1, 2), protocol.Marshal(f...), true)
		if e != nil {
			return nil, e
		}
		body := obFields(obBytes(reply, 1))
		if obNumber(body, 3) != p.UserID {
			return nil, fmt.Errorf("user info response identity mismatch")
		}
		properties := obFields(obBytes(body, 2))
		stringsMap := map[uint32]string{}
		nums := map[uint32]uint32{}
		for _, f := range properties {
			v := obFields(f.Data)
			switch f.Tag {
			case 1:
				nums[obNumber(v, 1)] = obNumber(v, 2)
			case 2:
				stringsMap[obNumber(v, 1)] = obString(v, 2)
			}
		}
		sex := "unknown"
		switch nums[20009] {
		case 1:
			sex = "male"
		case 2:
			sex = "female"
		}
		b.remember(p.UserID, "", stringsMap[20002])
		return map[string]any{"user_id": p.UserID, "nickname": stringsMap[20002], "remark": stringsMap[103], "long_nick": stringsMap[102], "sex": sex, "age": nums[20037]}, nil
	case "send_like":
		if p.UserID == 0 {
			return nil, fmt.Errorf("user_id required")
		}
		times := uint32(1)
		if p.Times != nil {
			times = *p.Times
		}
		if times < 1 || times > 10 {
			return nil, fmt.Errorf("times must be 1..10")
		}
		uid, e := b.uidOrErr(p.UserID)
		if e != nil {
			if _, e = b.oneBotFriends(ctx); e != nil {
				return nil, e
			}
			uid, e = b.uidOrErr(p.UserID)
			if e != nil {
				return nil, e
			}
		}
		body := protocol.Marshal(protocol.StringField(11, uid), protocol.VarintField(12, 71), protocol.VarintField(13, uint64(times)))
		return nil, b.oidbDo(ctx, protocol.BuildOIDB(protocol.OIDBRequest{Command: protocol.OIDBCommand{Service: 0x7e5, SubCommand: 104}, Body: body}), protocol.OidbCommand(0x7e5, 104))
	case "get_cookies", "get_csrf_token", "get_credentials":
		if p.Domain != "" && p.Domain != "qq.com" {
			return nil, fmt.Errorf("domain-specific p_skey is unavailable")
		}
		credentials := b.Credentials()
		self, e := b.Self(ctx)
		if e != nil {
			return nil, e
		}
		if credentials.Skey == "" {
			return nil, fmt.Errorf("QQ login did not supply skey")
		}
		var token uint32 = 5381
		for _, c := range []byte(credentials.Skey) {
			token += token<<5 + uint32(c)
		}
		token &= 0x7fffffff
		cookie := "uin=o" + strconv.FormatUint(uint64(self.Uin), 10) + "; skey=" + url.QueryEscape(credentials.Skey) + ";"
		if action == "get_cookies" {
			return map[string]any{"cookies": cookie}, nil
		}
		if action == "get_csrf_token" {
			return map[string]any{"token": token}, nil
		}
		return map[string]any{"cookies": cookie, "csrf_token": token}, nil
	default:
		return nil, fmt.Errorf("%w: %s", bot.ErrUnsupported, action)
	}
}

func (b *Bot) RecallPrivateMessage(ctx context.Context, target protocol.SendTarget, result protocol.SendResult) error {
	if target.IsTemporary() {
		return fmt.Errorf("temporary private recall has not been validated")
	}
	if result.ClientSequence == 0 || result.Timestamp == 0 {
		return fmt.Errorf("private recall metadata is missing")
	}
	uid := target.Uid
	if uid == "" {
		uid = b.uidFor(target.Uin)
	}
	if uid == "" {
		return fmt.Errorf("private recall peer UID is unknown")
	}
	ex, e := b.service()
	if e != nil {
		return e
	}
	_, e = ex.Exchange(ctx, protocol.RecallC2CCommand, protocol.BuildRecallC2C(uid, result.Sequence, result.Random, result.ClientSequence, result.Timestamp))
	if e != nil {
		return e
	}
	return nil
}
