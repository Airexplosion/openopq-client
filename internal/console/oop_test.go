package console

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"openopq/internal/protocol"
)

type oopTestBot struct {
	testBot
	replies []string
	target  protocol.SendTarget
}

func (b *oopTestBot) SendMessage(_ context.Context, target protocol.SendTarget, text string, _ []protocol.SendAt) (protocol.SendResult, error) {
	b.target = target
	b.replies = append(b.replies, text)
	return protocol.SendResult{}, nil
}
func TestOOPChatAndWebSettings(t *testing.T) {
	s := testServer(t, false, false)
	s.opt.TransportDir = t.TempDir()
	s.state.Account = "123456"
	b := new(oopTestBot)
	m := &protocol.IncomingMessage{IsGroup: true, GroupUin: 80001, SenderUin: 999, Content: "#oop"}
	event := protocol.MessageEvent{SelfUin: 123456, Msg: m}
	run := func() { s.oopCommand(context.Background(), b, event) }
	run()
	if len(b.replies) != 1 || !strings.Contains(b.replies[0], "运行时长") || b.target.Group != 80001 {
		t.Fatal("missing group status")
	}
	m.Content = "#oop off"
	run()
	if len(b.replies) != 1 || !s.oopEnabled("123456") {
		t.Fatal("visitor changed setting")
	}
	m.SenderUin = 123456
	run()
	if len(b.replies) != 2 || s.oopEnabled("123456") || !s.oopEnabled("111111") {
		t.Fatal("owner disable or account isolation failed")
	}
	next := New(s.opt)
	defer next.Close()
	if next.oopEnabled("123456") {
		t.Fatal("lost disabled setting after restart")
	}
	m.Content = "#oop"
	run()
	if len(b.replies) != 2 {
		t.Fatal("disabled command replied")
	}
	w := httptest.NewRecorder()
	s.control(w, httptest.NewRequest("POST", "/console/api/oop", strings.NewReader(`{"enabled":true}`)))
	if w.Code != 200 || !s.Snapshot().OOPEnabled {
		t.Fatalf("web enable: %d", w.Code)
	}
	m.IsGroup = false
	m.GroupUin = 0
	m.SenderUin = 999
	run()
	if len(b.replies) != 3 || b.target.Uin != 999 {
		t.Fatal("missing private status")
	}
}
