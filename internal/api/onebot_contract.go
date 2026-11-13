package api

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"time"

	"openopq/internal/bot"
	"openopq/internal/protocol"
)

// Wire contracts are shared by all v11 transports. Do not change OPQ or v12
// envelopes when adding fields required by a v11 consumer.
func (o *oneBotRuntime) statusData(ctx context.Context) map[string]any {
	self, err := o.s.bot.Self(ctx)
	online := err == nil && self.Online
	good := online
	if b, ok := o.s.bot.(interface{ HeartbeatStatus() bot.HeartbeatStatus }); ok {
		good = good && !b.HeartbeatStatus().Stale
	}
	return map[string]any{"online": online, "good": good, "time": int64(time.Since(o.startedAt) / time.Second)}
}

func oneBotGroupInfo(g bot.Group) map[string]any {
	muted := 0
	if g.ShutUpAllTimestamp == ^uint32(0) || int64(g.ShutUpAllTimestamp) > time.Now().Unix() {
		muted = -1
	}
	return map[string]any{
		"group_id": g.GroupUin, "group_name": g.GroupName, "group_remark": g.Remark,
		"member_count": g.MemberCount, "max_member_count": g.MaxMemberCount,
		"group_create_time": g.CreateTime, "group_level": 0, "group_memo": g.Memo,
		"group_description": g.Description, "group_all_shut": muted,
	}
}

func oneBotMessageKey(target protocol.SendTarget, sender, seq, at uint32) string {
	return fmt.Sprintf("msg:%d:%d:%d:%d:%d:%d", target.Group, target.TempGroup, target.Uin, sender, seq, at)
}

// get_msg returns the original event fields except its transport envelope.
// Use a new top-level map so serialization cannot mutate the cached event.
func cachedOneBotEvent(event map[string]any) map[string]any {
	out := make(map[string]any, len(event))
	for k, v := range event {
		if k != "post_type" && k != "self_id" {
			out[k] = v
		}
	}
	return out
}

func validateOneBotQuery(action string, raw json.RawMessage) error {
	var p map[string]json.RawMessage
	_ = json.Unmarshal(raw, &p)
	var ids []string
	switch action {
	case "get_stranger_info", "send_like", "set_friend_remark", "delete_friend", "mark_private_msg_as_read", "get_friend_msg_history":
		ids = []string{"user_id"}
	case "get_group_member_list", "mark_group_msg_as_read", "get_group_msg_history", "set_group_remark", "get_group_at_all_remain", "send_group_sign", "set_group_sign", "get_group_shut_list", "get_group_info_ex", "get_group_detail_info", "get_group_root_files", "get_group_file_system_info":
		ids = []string{"group_id"}
	case "get_group_files_by_folder", "delete_group_folder":
		ids = []string{"group_id"}
		var folder string
		if json.Unmarshal(p["folder_id"], &folder) != nil || folder == "" {
			return fmt.Errorf("folder_id is required")
		}
	case "get_group_file_url", "delete_group_file", "trans_group_file":
		ids = []string{"group_id"}
		var file string
		if json.Unmarshal(p["file_id"], &file) != nil || file == "" {
			return fmt.Errorf("file_id is required")
		}
	case "rename_group_file":
		ids = []string{"group_id"}
		var file, name string
		if json.Unmarshal(p["file_id"], &file) != nil || file == "" {
			return fmt.Errorf("file_id is required")
		}
		if json.Unmarshal(p["name"], &name) != nil || name == "" {
			_ = json.Unmarshal(p["new_name"], &name)
		}
		if name == "" {
			return fmt.Errorf("name is required")
		}
	case "move_group_file":
		ids = []string{"group_id"}
		var file string
		if json.Unmarshal(p["file_id"], &file) != nil || file == "" {
			return fmt.Errorf("file_id is required")
		}
		var dest string
		if json.Unmarshal(p["folder_id"], &dest) != nil || dest == "" {
			_ = json.Unmarshal(p["target_directory"], &dest)
		}
		if dest == "" {
			return fmt.Errorf("folder_id is required")
		}
	case "set_group_file_folder_name", "rename_group_file_folder":
		ids = []string{"group_id"}
		var folder, name string
		if json.Unmarshal(p["folder_id"], &folder) != nil || folder == "" {
			return fmt.Errorf("folder_id is required")
		}
		if json.Unmarshal(p["name"], &name) != nil || name == "" {
			if json.Unmarshal(p["folder_name"], &name) != nil || name == "" {
				_ = json.Unmarshal(p["new_name"], &name)
			}
		}
		if name == "" {
			return fmt.Errorf("name is required")
		}
	case "ocr_image", ".ocr_image":
		var image string
		if json.Unmarshal(p["image"], &image) != nil {
			_ = json.Unmarshal(p["file"], &image)
		}
		if image == "" {
			return fmt.Errorf("image is required")
		}
	case "download_file":
		var u string
		if json.Unmarshal(p["url"], &u) != nil || u == "" {
			return fmt.Errorf("url is required")
		}
	case "get_private_file_url":
		var file, hash string
		if json.Unmarshal(p["file_id"], &file) != nil || file == "" {
			return fmt.Errorf("file_id is required")
		}
		if json.Unmarshal(p["file_hash"], &hash) != nil || hash == "" {
			return fmt.Errorf("file_hash is required")
		}
	case "set_msg_emoji_like":
		var id int32
		if json.Unmarshal(p["message_id"], &id) != nil || id == 0 {
			return fmt.Errorf("valid message_id is required")
		}
		if jsonEmojiID(p["emoji_id"]) == "" {
			return fmt.Errorf("emoji_id is required")
		}
	case "set_essence_msg", "delete_essence_msg":
		var id int32
		if json.Unmarshal(p["message_id"], &id) != nil || id == 0 {
			return fmt.Errorf("valid message_id is required")
		}
	case "check_url_safely":
		var u string
		if json.Unmarshal(p["url"], &u) != nil || u == "" {
			return fmt.Errorf("url is required")
		}
	case "mark_msg_as_read":
		var id int32
		if json.Unmarshal(p["message_id"], &id) != nil || id == 0 {
			return fmt.Errorf("valid message_id is required")
		}
	case "get_group_member_info":
		ids = []string{"group_id", "user_id"}
	case "forward_group_single_msg":
		ids = []string{"group_id"}
		var id int32
		if json.Unmarshal(p["message_id"], &id) != nil || id == 0 {
			return fmt.Errorf("valid message_id is required")
		}
	case "forward_friend_single_msg":
		ids = []string{"user_id"}
		var id int32
		if json.Unmarshal(p["message_id"], &id) != nil || id == 0 {
			return fmt.Errorf("valid message_id is required")
		}
	case "set_group_kick_members", "create_group_file_folder":
		ids = []string{"group_id"}
	case "get_image", "get_record", "get_video", "get_file":
		var file, alias string
		if v, ok := p["file"]; ok && json.Unmarshal(v, &file) != nil {
			return fmt.Errorf("file must be a string")
		}
		if v, ok := p["file_id"]; ok && json.Unmarshal(v, &alias) != nil {
			return fmt.Errorf("file_id must be a string")
		}
		if file == "" && alias == "" {
			return fmt.Errorf("file is required")
		}
	case "set_friend_add_request", "set_group_add_request":
		var flag string
		if json.Unmarshal(p["flag"], &flag) != nil || flag == "" {
			return fmt.Errorf("flag is required")
		}
		var approve bool
		if v, ok := p["approve"]; ok && (string(v) == "null" || json.Unmarshal(v, &approve) != nil) {
			return fmt.Errorf("approve must be a boolean")
		}
	}
	for _, k := range ids {
		var id uint64
		if json.Unmarshal(p[k], &id) != nil || id == 0 || id > math.MaxUint32 {
			return fmt.Errorf("valid %s is required", k)
		}
	}
	return nil
}

func jsonEmojiID(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil && s != "" {
		return s
	}
	var n uint64
	if json.Unmarshal(raw, &n) == nil && n > 0 {
		return strconv.FormatUint(n, 10)
	}
	return ""
}

func (s *Server) oneBotForwardResponse(ctx context.Context, messages []*protocol.IncomingMessage, echo json.RawMessage) obResponse {
	self, err := s.bot.Self(ctx)
	if err != nil {
		return obFail(err.Error(), echo)
	}
	out := make([]map[string]any, 0, len(messages))
	for _, m := range messages {
		if m == nil {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal(s.ob.event(protocol.MessageEvent{SelfUin: self.Uin, Msg: m}), &event); err != nil {
			return obFail("invalid forwarded message", echo)
		}
		out = append(out, event)
	}
	return obOK(map[string]any{"messages": out}, echo)
}
