package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	"openopq/internal/protocol"
)

type obRichTestBot struct {
	*obTestBot
	parts   []protocol.SendSegment
	actions []obAction
}

func (b *obRichTestBot) SendRichMessage(_ context.Context, _ protocol.SendTarget, s []protocol.SendSegment) (protocol.SendResult, error) {
	b.parts = s
	return protocol.SendResult{Sequence: 9, Timestamp: 123, ClientSequence: 2}, nil
}
func (b *obRichTestBot) OneBotAction(_ context.Context, a string, p json.RawMessage) (any, error) {
	b.actions = append(b.actions, obAction{Action: a, Params: p})
	return map[string]any{"file": "test-cache-file"}, nil
}
func TestCompletionAPIRoutingAndRichOrder(t *testing.T) {
	_, base := newOBTest(t)
	b := &obRichTestBot{obTestBot: base}
	s := NewServer(b, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{OneBot: true})
	defer s.Close()
	response := obHTTP(s, "POST", "/send_private_msg", `{"user_id":123,"message":[{"type":"text","data":{"text":"before"}},{"type":"image","data":{"file":"base64://eA=="}},{"type":"face","data":{"id":"14"}},{"type":"image","data":{"file":"base64://eQ=="}},{"type":"text","data":{"text":"after"}}]}`, "application/json")
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"status":"ok"`) {
		t.Fatal(response.Body.String())
	}
	if len(b.parts) != 5 || string(b.parts[1].Bytes) != "x" || string(b.parts[3].Bytes) != "y" || b.parts[4].Text != "after" {
		t.Fatal(b.parts)
	}
	for _, c := range []struct{ action, params string }{{"set_friend_add_request", `{"flag":"a","approve":false}`}, {"set_group_add_request", `{"flag":"b","sub_type":"invite","approve":true}`}, {"get_forward_msg", `{"id":"forward-test"}`}, {"get_image", `{"file":"image-test"}`}, {"get_record", `{"file":"record-test","out_format":"mp3"}`}, {"get_group_system_msg", `{}`}, {"clean_cache", `{}`}} {
		r := obHTTP(s, "POST", "/"+c.action, c.params, "application/json")
		if r.Code != 200 || !strings.Contains(r.Body.String(), `"status":"ok"`) {
			t.Fatal(c.action, r.Body.String())
		}
		if b.actions[len(b.actions)-1].Action != c.action {
			t.Fatal("action routed to unrelated handler")
		}
	}
	r := s.quickOperation(context.Background(), json.RawMessage(`{"self_id":10001,"post_type":"request","request_type":"friend","flag":"test-flag"}`), json.RawMessage(`{"approve":"false"}`))
	if r.Status != "failed" {
		t.Fatal("invalid quick-operation boolean could reject a real request")
	}
}
func TestStandardRequestEventShape(t *testing.T) {
	out := onebotNoticeJSON(protocol.MessageEvent{SelfUin: 10001, Notice: &protocol.Notice{Kind: protocol.NoticeGroupRequest, GroupUin: 90001, UserUin: 123, Flag: "opaque", SubType: "invite", Comment: "test"}})
	var event map[string]any
	_ = json.Unmarshal(out, &event)
	if event["post_type"] != "request" || event["request_type"] != "group" || event["sub_type"] != "invite" || event["flag"] != "opaque" || event["self_id"] != float64(10001) {
		t.Fatal(string(out))
	}
	if onebotNoticeJSON(protocol.MessageEvent{Notice: &protocol.Notice{Kind: protocol.NoticeFriendRequest, UserUin: 123}}) != nil {
		t.Fatal("request without an actionable flag was emitted")
	}
}
