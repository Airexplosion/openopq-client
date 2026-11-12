package api

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
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
	case "get_stranger_info", "send_like":
		ids = []string{"user_id"}
	case "get_group_member_list":
		ids = []string{"group_id"}
	case "get_group_member_info":
		ids = []string{"group_id", "user_id"}
	case "get_image", "get_record":
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
