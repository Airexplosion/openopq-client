package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"openopq/internal/protocol"
)

type compatTestBot struct {
	*obRichTestBot
	forwardID string
}

func (b *compatTestBot) OneBotAction(_ context.Context, action string, raw json.RawMessage) (any, error) {
	if action != "get_forward_msg" {
		return nil, fmt.Errorf("unexpected action %s", action)
	}
	var p struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(raw, &p)
	b.forwardID = p.ID
	return map[string]any{"message": []any{map[string]any{"type": "node", "data": map[string]any{"content": "forward body"}}}}, nil
}
func compatServer(t *testing.T) (*Server, *compatTestBot) {
	_, base := newOBTest(t)
	b := &compatTestBot{obRichTestBot: &obRichTestBot{obTestBot: base}}
	s := NewServer(b, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{OneBot: true})
	t.Cleanup(func() { _ = s.Close() })
	s.ob.mu.Lock()
	s.ob.config.MessageFormat = "string"
	s.ob.mu.Unlock()
	return s, b
}
func cacheCompatMessage(s *Server, m *protocol.IncomingMessage) int32 {
	var event struct {
		ID int32 `json:"message_id"`
	}
	_ = json.Unmarshal(s.ob.event(protocol.MessageEvent{SelfUin: 10001, Msg: m}), &event)
	return event.ID
}
func TestAgentQuotedSendAndConversationIsolation(t *testing.T) {
	s, b := compatServer(t)
	id := cacheCompatMessage(s, &protocol.IncomingMessage{SenderUin: 123, SenderUid: "u_peer", MsgSeq: 17, MsgTime: 100, Content: "original"})
	for _, c := range []struct {
		path, params string
		success      bool
	}{
		{"send_private_msg", fmt.Sprintf(`{"user_id":123,"message":[{"type":"reply","data":{"id":"%d"}},{"type":"text","data":{"text":"answer"}}]}`, id), true},
		{"send_private_msg", fmt.Sprintf(`{"user_id":124,"message":"[CQ:reply,id=%d]wrong peer"}`, id), false},
		{"send_group_msg", fmt.Sprintf(`{"group_id":123,"message":"[CQ:reply,id=%d]wrong kind"}`, id), false},
		{"send_private_msg", `{"user_id":123,"message":"[CQ:reply,id=1]missing"}`, false},
		{"send_private_msg", fmt.Sprintf(`{"user_id":123,"message":"[CQ:reply,id=%d][CQ:reply,id=%d]duplicate"}`, id, id), false},
	} {
		r := obHTTP(s, "POST", "/"+c.path, c.params, "application/json")
		if strings.Contains(r.Body.String(), `"status":"ok"`) != c.success {
			t.Fatal(c.params, r.Body.String())
		}
	}
	if len(b.parts) != 2 || b.parts[0].Reply == nil || b.parts[0].Reply.Sequence != 17 || b.parts[0].Reply.SenderUid != "u_peer" || b.parts[1].Text != "answer" {
		t.Fatal(b.parts)
	}
	s.ob.mu.Lock()
	m := s.ob.cache[id]
	m.Created = time.Now().Add(-2 * time.Hour)
	s.ob.cache[id] = m
	s.ob.mu.Unlock()
	if _, err := s.cachedReply(id, protocol.SendTarget{Uin: 123}); err == nil {
		t.Fatal("expired quote accepted")
	}
}
func TestAgentGroupQuoteAndEmbeddedContext(t *testing.T) {
	s, _ := compatServer(t)
	q := &protocol.SendReply{Sequence: 81, SenderUin: 124, Timestamp: 90, Elements: [][]byte{protocol.TextElem("earlier context")}}
	id := cacheCompatMessage(s, &protocol.IncomingMessage{IsGroup: true, GroupUin: 90001, SenderUin: 123, MsgSeq: 82, MsgTime: 100, Content: "followup", ReplyTo: q})
	s.ob.mu.Lock()
	m := s.ob.cache[id]
	s.ob.mu.Unlock()
	text, _ := m.Data["message"].(string)
	var quoteID int32
	_, _ = fmt.Sscanf(text, "[CQ:reply,id=%d]", &quoteID)
	if quoteID == 0 {
		t.Fatal(m.Data)
	}
	r := obHTTP(s, "POST", "/get_msg", fmt.Sprintf(`{"message_id":%d}`, quoteID), "application/json")
	if !strings.Contains(r.Body.String(), "earlier context") {
		t.Fatal(r.Body.String())
	}
	if _, err := s.cachedReply(quoteID, protocol.SendTarget{Group: 90001}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.cachedReply(quoteID, protocol.SendTarget{Group: 90002}); err == nil {
		t.Fatal("cross-group quote accepted")
	}
}
func TestAgentForwardMessageIDAndStandardResourceID(t *testing.T) {
	for _, array := range []bool{false, true} {
		s, b := compatServer(t)
		if array {
			s.ob.mu.Lock()
			s.ob.config.MessageFormat = "array"
			s.ob.mu.Unlock()
		}
		id := cacheCompatMessage(s, &protocol.IncomingMessage{SenderUin: 123, MsgSeq: 1, MsgTime: 100, Segments: []protocol.Segment{{Type: "forward", Data: map[string]string{"id": "resource-abc"}}}})
		for _, p := range []string{fmt.Sprintf(`{"message_id":%d}`, id), fmt.Sprintf(`{"message_id":"%d"}`, id), `{"id":"resource-abc"}`} {
			r := obHTTP(s, "POST", "/get_forward_msg", p, "application/json")
			if b.forwardID != "resource-abc" || !strings.Contains(r.Body.String(), `"messages":`) || !strings.Contains(r.Body.String(), `"message":`) {
				t.Fatal(r.Body.String(), b.forwardID)
			}
		}
		b.forwardID = ""
		r := obHTTP(s, "POST", "/get_forward_msg", `{"message_id":1}`, "application/json")
		if strings.Contains(r.Body.String(), `"status":"ok"`) || b.forwardID != "" {
			t.Fatal("missing message reached backend", r.Body.String())
		}
	}
}

func TestQuoteKeepsExistingEventAndTemporaryConversationScope(t *testing.T) {
	s, _ := compatServer(t)
	m := &protocol.IncomingMessage{SenderUin: 123, SenderNick: "original nickname", MsgSeq: 17, MsgTime: 100, Content: "full message", IsTemporary: true, TempGroupUin: 90001}
	id := cacheCompatMessage(s, m)
	target := protocol.SendTarget{Uin: 123, TempGroup: 90001}
	quoteID := s.ob.rememberQuote(target, protocol.SendReply{Sequence: 17, SenderUin: 123, Timestamp: 100, Elements: [][]byte{protocol.TextElem("truncated preview")}})
	if quoteID != id {
		t.Fatal("quote did not reuse the original event", id, quoteID)
	}
	s.ob.mu.Lock()
	cached := s.ob.cache[id]
	s.ob.mu.Unlock()
	if cached.Data["raw_message"] != "full message" || cached.Data["sender"].(map[string]any)["nickname"] != "original nickname" {
		t.Fatal("quote replaced full event", cached.Data)
	}
	for _, wrong := range []protocol.SendTarget{{Uin: 123}, {Uin: 123, TempGroup: 90002}, {Uin: 124, TempGroup: 90001}} {
		if _, err := s.cachedReply(id, wrong); err == nil {
			t.Fatal("quote crossed conversation scope", wrong)
		}
	}
	if _, err := s.cachedReply(id, target); err != nil {
		t.Fatal(err)
	}
}
