package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"openopq/internal/protocol"
)

type temporaryAPIBot struct {
	*obTestBot
	last protocol.SendTarget
	fail bool
}

func (b *temporaryAPIBot) PrepareSendTarget(_ context.Context, target protocol.SendTarget) (protocol.SendTarget, error) {
	if b.fail {
		return target, fmt.Errorf("source-group membership could not be confirmed")
	}
	if target.IsTemporary() {
		target.Uid = "u_peer"
	}
	return target, nil
}
func (b *temporaryAPIBot) SendRichMessage(_ context.Context, target protocol.SendTarget, _ []protocol.SendSegment) (protocol.SendResult, error) {
	b.last = target
	return protocol.SendResult{Sequence: 9, Timestamp: 123, ClientSequence: 4, Random: 5}, nil
}
func (b *temporaryAPIBot) SendMessage(ctx context.Context, target protocol.SendTarget, text string, ats []protocol.SendAt) (protocol.SendResult, error) {
	b.last = target
	return b.obTestBot.SendMessage(ctx, target, text, ats)
}
func newTemporaryAPI(t *testing.T) (*Server, *temporaryAPIBot) {
	_, base := newOBTest(t)
	b := &temporaryAPIBot{obTestBot: base}
	s := NewServer(b, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{OneBot: true, OneBot12: true})
	t.Cleanup(func() { s.Close() })
	return s, b
}
func TestTemporaryOneBotPrivateSendAndCachedContext(t *testing.T) {
	s, b := newTemporaryAPI(t)
	for _, action := range []string{"send_private_msg", "send_msg"} {
		response := s.handleOneBotAction(context.Background(), obAction{Action: action, Params: json.RawMessage(`{"message_type":"private","user_id":"123","group_id":"90001","message":"hello"}`)})
		if response.Status != "ok" || b.last.Group != 0 || b.last.Uin != 123 || b.last.TempGroup != 90001 {
			t.Fatal(response, b.last)
		}
		id := response.Data.(map[string]any)["message_id"].(int32)
		cached := s.handleOneBotAction(context.Background(), obAction{Action: "get_msg", Params: json.RawMessage(fmt.Sprintf(`{"message_id":%d}`, id))})
		data := cached.Data.(map[string]any)
		if data["message_type"] != "private" || data["sub_type"] != "group" || data["sender"].(map[string]any)["group_id"] != float64(90001) {
			t.Fatal(data)
		}
		if _, exists := data["group_id"]; exists {
			t.Fatal("temporary private message masquerades as group chat", data)
		}
		r := s.deleteOneBotMessage(context.Background(), id, nil)
		if r.Status != "failed" || !r.Unavailable || len(b.recalls) != 0 {
			t.Fatal("unverified temporary recall used ordinary C2C", r)
		}
	}
}
func TestTemporaryIncomingQuickReplyAndCacheIsolation(t *testing.T) {
	s, b := newTemporaryAPI(t)
	ids := map[float64]bool{}
	for _, group := range []uint32{90001, 90002} {
		m := &protocol.IncomingMessage{IsTemporary: true, TempGroupUin: group, SenderUin: 123, SenderUid: "u_peer", MsgSeq: 17, MsgTime: 123, Content: "hello"}
		raw := s.ob.event(protocol.MessageEvent{SelfUin: 10001, Msg: m})
		var event map[string]any
		_ = json.Unmarshal(raw, &event)
		id := event["message_id"].(float64)
		if ids[id] || event["sub_type"] != "group" || event["sender"].(map[string]any)["group_id"] != float64(group) {
			t.Fatal(event)
		}
		ids[id] = true
		r := s.quickOperation(context.Background(), raw, json.RawMessage(`{"reply":"reply"}`))
		if r.Status != "ok" || b.last.TempGroup != group || b.last.IsGroup() {
			t.Fatal("quick reply lost source group", r, b.last)
		}
	}
}
func TestTemporaryInvalidRecipientDoesNotFetchMedia(t *testing.T) {
	s, b := newTemporaryAPI(t)
	b.fail = true
	fetched := 0
	media := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fetched++; _, _ = w.Write([]byte("image")) }))
	defer media.Close()
	params, _ := json.Marshal(map[string]any{"user_id": 123, "group_id": 90001, "message": []map[string]any{{"type": "image", "data": map[string]string{"file": media.URL}}}})
	r := s.handleOneBotAction(context.Background(), obAction{Action: "send_private_msg", Params: params})
	if r.Status != "failed" || fetched != 0 || b.last.Uin != 0 {
		t.Fatal("invalid source group caused media fetch/send", r, fetched, b.last)
	}
	legacy, _ := newOBTest(t)
	r = legacy.handleOneBotAction(context.Background(), obAction{Action: "send_private_msg", Params: json.RawMessage(`{"user_id":123,"group_id":90001,"message":"hello"}`)})
	if r.Status != "failed" {
		t.Fatal("legacy backend silently sent ordinary/group text", r)
	}
}
func TestOPQTemporaryPrivateSendUsesGroupCode(t *testing.T) {
	s, b := newTemporaryAPI(t)
	_, err := s.handleSend(context.Background(), json.RawMessage(`{"CgiCmd":"MessageSvc.PbSendMsg","CgiRequest":{"ToType":3,"ToUin":123,"GroupCode":90001,"Content":"hello"}}`))
	if err != nil || b.last.TempGroup != 90001 || b.last.IsGroup() || b.last.Uin != 123 {
		t.Fatal(b.last, err)
	}
	for _, raw := range []string{`{"ToType":3,"ToUin":123,"Content":"hello"}`, `{"ToType":3,"ToUin":123,"GroupCode":90001,"GroupID":90002,"Content":"hello"}`, `{"ToType":1,"ToUin":123,"GroupCode":90001,"Content":"hello"}`} {
		if _, err := s.handleSend(context.Background(), json.RawMessage(raw)); err == nil {
			t.Fatal("ambiguous OPQ recipient accepted", raw)
		}
	}
	var event map[string]any
	_ = json.Unmarshal(opqEventJSON(protocol.MessageEvent{SelfUin: 10001, Msg: &protocol.IncomingMessage{IsTemporary: true, TempGroupUin: 90001, SenderUin: 123}}), &event)
	head := event["CurrentPacket"].(map[string]any)["EventData"].(map[string]any)["MsgHead"].(map[string]any)
	if head["MsgType"] != float64(141) || head["FromType"] != float64(3) || head["C2CTempMessageHead"].(map[string]any)["GroupCode"] != float64(90001) {
		t.Fatal(head)
	}
}
func TestV12TemporaryPrivateExtension(t *testing.T) {
	s, b := newTemporaryAPI(t)
	r := request12(t, s, `{"action":"send_message","params":{"detail_type":"private","user_id":"123","qq.temp_group_id":"90001","message":[{"type":"text","data":{"text":"hello"}}]}}`)
	if r.Retcode != 0 || b.last.TempGroup != 90001 || b.last.IsGroup() {
		t.Fatal(r, b.last)
	}
	s.v12.ingest(onebotEventJSON(protocol.MessageEvent{SelfUin: 10001, Msg: &protocol.IncomingMessage{IsTemporary: true, TempGroupUin: 90001, SenderUin: 123, MsgSeq: 7, MsgTime: 123, Content: "hello"}}))
	s.v12.mu.Lock()
	raw, _ := json.Marshal(s.v12.events)
	s.v12.mu.Unlock()
	if !strings.Contains(string(raw), `"qq.temp_group_id":"90001"`) {
		t.Fatal(string(raw))
	}
}
