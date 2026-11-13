package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"openopq/internal/bot"
	"openopq/internal/protocol"
)

// NewOneBotQueryBackend adapts an already authenticated exchange. It performs no
// login, registration, heartbeat or signer setup and does not own the exchange.
// Used by the upgrade gateway to keep existing QQ connections intact.
func NewOneBotQueryBackend(exchange Exchange) *Bot { return &Bot{exchange: exchange} }

func (b *Bot) SupportedOneBotActions() []string {
	return []string{"set_friend_add_request", "set_group_add_request", "get_forward_msg", "get_image", "get_record", "get_video", "get_file", "get_group_system_msg", "get_group_ignore_add_request", "get_group_ignored_notices", "clean_cache", "get_friend_list", "get_friends_with_category", "get_group_member_list", "get_group_member_info", "get_stranger_info", "send_like", "get_cookies", "get_csrf_token", "get_credentials", "get_clientkey", "set_friend_remark", "set_group_remark", "delete_friend", "mark_msg_as_read", "mark_group_msg_as_read", "mark_private_msg_as_read", "get_group_msg_history", "get_friend_msg_history", "check_url_safely", "get_group_at_all_remain", "send_group_sign", "set_group_sign", "get_group_shut_list", "get_group_info_ex", "get_group_detail_info", "get_rkey", "nc_get_rkey", "ocr_image", ".ocr_image", "get_group_root_files", "get_group_files_by_folder", "get_group_file_url", "get_group_file_system_info", "delete_group_file", "create_group_file_folder", "delete_group_folder", "move_group_file", "rename_group_file", "set_group_file_folder_name", "rename_group_file_folder", "trans_group_file", "get_private_file_url", "download_file", "nc_get_packet_status", "get_online_clients"}
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
			props, category := map[uint32]string{}, map[uint32]string{}
			for _, a := range v {
				if a.Tag != 10001 || !a.IsData {
					continue
				}
				af := obFields(a.Data)
				dest := props
				switch obNumber(af, 1) {
				case 1:
					dest = props
				case 4:
					dest = category
				default:
					continue
				}
				for _, p := range obFields(obBytes(af, 2)) {
					if p.Tag == 2 && p.IsData {
						pf := obFields(p.Data)
						dest[obNumber(pf, 1)] = obString(pf, 2)
					}
				}
			}
			b.remember(uin, uid, props[20002])
			catID, _ := strconv.ParseUint(category[100], 10, 32)
			out = append(out, map[string]any{
				"user_id": uin, "nickname": props[20002], "remark": props[103],
				"category_id": uint32(catID), "category_name": category[101],
			})
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
	case "set_friend_add_request", "set_group_add_request", "get_group_system_msg", "get_group_ignore_add_request", "get_group_ignored_notices":
		return b.requestAction(ctx, action, raw)
	case "get_forward_msg":
		return b.forwardAction(ctx, raw)
	case "clean_cache":
		return nil, b.cleanMedia()
	case "get_image", "get_record", "get_video", "get_file":
		return b.mediaAction(ctx, action, raw)
	case "get_friend_list":
		return b.oneBotFriends(ctx)
	case "get_friends_with_category":
		friends, err := b.oneBotFriends(ctx)
		if err != nil {
			return nil, err
		}
		return groupFriendsByCategory(friends), nil
	case "get_group_shut_list":
		if p.GroupID == 0 {
			return nil, fmt.Errorf("group_id required")
		}
		members, err := b.oneBotMembers(ctx, p.GroupID)
		if err != nil {
			return nil, err
		}
		now := uint32(time.Now().Unix())
		out := []map[string]any{}
		for _, m := range members {
			stamp, _ := m["shut_up_timestamp"].(uint32)
			if stamp == 0 || (stamp != ^uint32(0) && stamp <= now) {
				continue
			}
			out = append(out, m)
		}
		return out, nil
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
	case "set_friend_remark":
		var extra struct {
			Remark string `json:"remark"`
		}
		if json.Unmarshal(raw, &extra) != nil {
			return nil, fmt.Errorf("invalid action parameters")
		}
		if p.UserID == 0 {
			return nil, fmt.Errorf("user_id required")
		}
		if len(extra.Remark) > 128 {
			return nil, fmt.Errorf("remark is too long")
		}
		return nil, b.SetFriendRemark(ctx, p.UserID, extra.Remark)
	case "set_group_remark":
		var extra struct {
			Remark string `json:"remark"`
		}
		if json.Unmarshal(raw, &extra) != nil {
			return nil, fmt.Errorf("invalid action parameters")
		}
		if p.GroupID == 0 {
			return nil, fmt.Errorf("group_id required")
		}
		if len(extra.Remark) > 128 {
			return nil, fmt.Errorf("remark is too long")
		}
		return nil, b.SetGroupRemark(ctx, p.GroupID, extra.Remark)
	case "get_group_at_all_remain":
		if p.GroupID == 0 {
			return nil, fmt.Errorf("group_id required")
		}
		info, e := b.AtAllRemain(ctx, p.GroupID)
		if e != nil {
			return nil, e
		}
		return map[string]any{
			"can_at_all":                    info.CanAtAll,
			"remain_at_all_count_for_uin":   info.CountForUin,
			"remain_at_all_count_for_group": info.CountForGroup,
		}, nil
	case "check_url_safely":
		var extra struct {
			URL string `json:"url"`
		}
		if json.Unmarshal(raw, &extra) != nil || extra.URL == "" {
			return nil, fmt.Errorf("url required")
		}
		if len(extra.URL) > 2048 || strings.ContainsAny(extra.URL, "\x00\r\n") {
			return nil, fmt.Errorf("invalid url")
		}
		level, e := b.CheckURL(ctx, extra.URL)
		if e != nil {
			return nil, e
		}
		return map[string]any{"level": level}, nil
	case "send_group_sign", "set_group_sign":
		if p.GroupID == 0 {
			return nil, fmt.Errorf("group_id required")
		}
		info, e := b.GroupSign(ctx, p.GroupID)
		if e != nil {
			return nil, e
		}
		return map[string]any{
			"title": info.Title, "keep_day_text": info.KeepDayText,
			"group_rank_text": info.GroupRank, "detail_url": info.DetailURL,
		}, nil
	case "get_clientkey":
		key, e := b.ClientKey(ctx)
		if e != nil {
			return nil, e
		}
		return map[string]any{"clientkey": key.Key}, nil
	case "delete_friend":
		var extra struct {
			Block bool `json:"block"`
		}
		_ = json.Unmarshal(raw, &extra)
		if p.UserID == 0 {
			return nil, fmt.Errorf("user_id required")
		}
		return nil, b.DeleteFriend(ctx, p.UserID, extra.Block)
	case "mark_group_msg_as_read":
		var extra struct {
			Seq uint32 `json:"message_seq"`
		}
		_ = json.Unmarshal(raw, &extra)
		if p.GroupID == 0 {
			return nil, fmt.Errorf("group_id required")
		}
		if extra.Seq == 0 {
			return nil, fmt.Errorf("message_seq required")
		}
		return nil, b.MarkGroupRead(ctx, p.GroupID, extra.Seq)
	case "mark_private_msg_as_read":
		var extra struct {
			Seq  uint32 `json:"message_seq"`
			Time uint32 `json:"time"`
		}
		_ = json.Unmarshal(raw, &extra)
		if p.UserID == 0 {
			return nil, fmt.Errorf("user_id required")
		}
		if extra.Seq == 0 {
			return nil, fmt.Errorf("message_seq required")
		}
		if extra.Time == 0 {
			extra.Time = uint32(time.Now().Unix())
		}
		return nil, b.MarkPrivateRead(ctx, p.UserID, extra.Time, extra.Seq)
	case "get_group_msg_history":
		var extra struct {
			Seq   uint32 `json:"message_seq"`
			Count uint32 `json:"count"`
		}
		_ = json.Unmarshal(raw, &extra)
		if p.GroupID == 0 {
			return nil, fmt.Errorf("group_id required")
		}
		if extra.Seq == 0 {
			return []*protocol.IncomingMessage{}, nil
		}
		count := extra.Count
		if count == 0 {
			count = 20
		}
		if count > 28 {
			count = 28
		}
		start := extra.Seq + 1 - count
		if start < 1 || start > extra.Seq {
			start = 1
		}
		return b.GroupMessages(ctx, p.GroupID, start, extra.Seq)
	case "get_friend_msg_history":
		var extra struct {
			Time  uint32 `json:"time"`
			Count uint32 `json:"count"`
		}
		_ = json.Unmarshal(raw, &extra)
		if p.UserID == 0 {
			return nil, fmt.Errorf("user_id required")
		}
		count := extra.Count
		if count == 0 {
			count = 20
		}
		if count > 28 {
			count = 28
		}
		if extra.Time == 0 {
			extra.Time = uint32(time.Now().Unix())
		}
		return b.FriendMessages(ctx, p.UserID, extra.Time, count)
	case "get_cookies", "get_csrf_token", "get_credentials":
		if p.Domain != "" && (len(p.Domain) > 253 || strings.ContainsAny(p.Domain, "\x00\r\n /")) {
			return nil, fmt.Errorf("invalid domain")
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
		uin := "o" + strconv.FormatUint(uint64(self.Uin), 10)
		cookie := "uin=" + uin + "; skey=" + url.QueryEscape(credentials.Skey) + ";"
		if p.Domain != "" && p.Domain != "qq.com" {
			cookies, e := b.DomainCookies(ctx, p.Domain)
			if e != nil {
				return nil, e
			}
			pskey := ""
			for _, c := range cookies {
				if c.Value != "" {
					pskey = c.Value
					break
				}
			}
			if pskey == "" {
				return nil, fmt.Errorf("no p_skey for domain %s", p.Domain)
			}
			cookie += " p_uin=" + uin + "; p_skey=" + url.QueryEscape(pskey) + ";"
		}
		if action == "get_cookies" {
			return map[string]any{"cookies": cookie}, nil
		}
		if action == "get_csrf_token" {
			return map[string]any{"token": token}, nil
		}
		return map[string]any{"cookies": cookie, "csrf_token": token}, nil
	case "get_rkey", "nc_get_rkey", "ocr_image", ".ocr_image", "get_group_info_ex", "get_group_detail_info",
		"get_group_root_files", "get_group_files_by_folder", "get_group_file_url",
		"get_group_file_system_info", "delete_group_file", "create_group_file_folder",
		"delete_group_folder", "move_group_file", "rename_group_file",
		"set_group_file_folder_name", "rename_group_file_folder", "trans_group_file",
		"get_private_file_url", "download_file":
		return b.oneBotFileAction(ctx, action, raw)
	case "nc_get_packet_status":
		if _, err := b.service(); err != nil {
			return nil, err
		}
		return true, nil
	case "get_online_clients":
		dev := b.Device()
		name := dev.DeviceName
		if name == "" {
			name = "openopq"
		}
		kind := "PC"
		if strings.Contains(strings.ToLower(dev.OS), "mac") {
			kind = "Mac"
		} else if strings.Contains(strings.ToLower(dev.OS), "linux") {
			kind = "Linux"
		}
		appID := b.account.AppID
		if appID == 0 {
			appID = dev.AppID
		}
		return map[string]any{"clients": []map[string]any{{
			"app_id": appID, "device_name": name, "device_kind": kind,
		}}}, nil
	default:
		return nil, fmt.Errorf("%w: %s", bot.ErrUnsupported, action)
	}
}

func groupFriendsByCategory(friends []map[string]any) []map[string]any {
	order := []uint32{}
	grouped := map[uint32]map[string]any{}
	for _, f := range friends {
		id, _ := f["category_id"].(uint32)
		name, _ := f["category_name"].(string)
		g, ok := grouped[id]
		if !ok {
			if name == "" && id == 0 {
				name = "默认分组"
			}
			g = map[string]any{"category_id": id, "category_name": name, "friends": []map[string]any{}}
			grouped[id] = g
			order = append(order, id)
		}
		g["friends"] = append(g["friends"].([]map[string]any), f)
	}
	out := make([]map[string]any, 0, len(order))
	for _, id := range order {
		out = append(out, grouped[id])
	}
	return out
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
