package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"openopq/internal/bot"
	"openopq/internal/protocol"
)

type obTestBot struct {
	*bot.Stub
	mu      sync.Mutex
	sent    []string
	targets []protocol.SendTarget
	recalls []protocol.SendTarget
	events  chan protocol.MessageEvent
}

func (b *obTestBot) Self(context.Context) (bot.Self, error) {
	return bot.Self{Uin: 10001, Nickname: "test", Online: true}, nil
}
func (b *obTestBot) Groups(context.Context) ([]bot.Group, error) {
	return []bot.Group{{GroupUin: 90001, GroupName: "test", MemberCount: 3, MaxMemberCount: 500}}, nil
}
func (b *obTestBot) SendMessage(_ context.Context, target protocol.SendTarget, text string, ats []protocol.SendAt) (protocol.SendResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sent = append(b.sent, text)
	b.targets = append(b.targets, target)
	return protocol.SendResult{Sequence: 7, Timestamp: 100, ClientSequence: 3, Random: 4}, nil
}
func (b *obTestBot) RecallGroupMessage(_ context.Context, group, seq uint32) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.recalls = append(b.recalls, protocol.SendTarget{Group: group})
	return nil
}
func (b *obTestBot) RecallPrivateMessage(_ context.Context, target protocol.SendTarget, res protocol.SendResult) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.recalls = append(b.recalls, target)
	return nil
}
func (b *obTestBot) Subscribe() (<-chan protocol.MessageEvent, func()) { return b.events, func() {} }
func newOBTest(t *testing.T) (*Server, *obTestBot) {
	t.Helper()
	b := &obTestBot{Stub: bot.NewStub(), events: make(chan protocol.MessageEvent, 128)}
	s := NewServer(b, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{OneBot: true})
	t.Cleanup(func() { s.Close() })
	return s, b
}
func obHTTP(s *Server, method, path, body, ct string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if ct != "" {
		r.Header.Set("Content-Type", ct)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func TestOneBotHTTPStandardEncodings(t *testing.T) {
	s, b := newOBTest(t)
	for _, c := range []struct{ method, path, body, ct string }{{"GET", "/send_private_msg?user_id=123&message=%5BCQ%3Aat%2Cqq%3Dall%5D&auto_escape=true", "", ""}, {"POST", "/onebot/v11/send_private_msg/", "user_id=123&message=hello", "application/x-www-form-urlencoded"}, {"POST", "/onebot/v11/", `{"action":"send_private_msg","params":{"user_id":"123","message":{"type":"text","data":{"text":"hello"}}}}`, "application/json"}} {
		w := obHTTP(s, c.method, c.path, c.body, c.ct)
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"ok"`) {
			t.Fatalf("%s: %d %s", c.path, w.Code, w.Body.String())
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.sent) != 3 || b.sent[0] != "[CQ:at,qq=all]" || b.targets[2].Uin != 123 {
		t.Fatal("parameter or auto_escape mismatch", b.sent)
	}
}
func TestOneBotHTTPErrorCodesAndNoLossySend(t *testing.T) {
	s, b := newOBTest(t)
	for _, c := range []struct {
		method, path, body, ct string
		code                   int
	}{{"GET", "/onebot/v11/nonexistent", "", "", 200}, {"POST", "/get_status", "{}", "text/plain", 400}, {"POST", "/send_msg", "{", "application/json", 400}, {"GET", "/get_status?x=1&x=2", "", "", 400}, {"POST", "/send_msg", `{"user_id":4294967296,"message":"x"}`, "application/json", 200}, {"POST", "/send_msg", `{"user_id":123,"message":[{"type":"video","data":{"file":"x"}}]}`, "application/json", 200}, {"POST", "/send_msg", `{"user_id":123,"message":"[CQ:image,file=x][CQ:image,file=y]"}`, "application/json", 200}} {
		w := obHTTP(s, c.method, c.path, c.body, c.ct)
		if w.Code != c.code {
			t.Fatalf("%s expected %d got %d: %s", c.path, c.code, w.Code, w.Body.String())
		}
		var response obResponse
		if json.Unmarshal(w.Body.Bytes(), &response) != nil || response.Status != "failed" || response.Retcode == 0 || response.Message == "" {
			t.Fatalf("expected structured failure without a send: %s", w.Body.String())
		}
	}
	if len(b.sent) != 0 {
		t.Fatal("invalid message triggered real send")
	}
	r := s.handleOneBotAction(context.Background(), obAction{Action: "bad", Echo: json.RawMessage(`{"id":7}`)})
	if r.Retcode != 1404 || string(r.Echo) != `{"id":7}` {
		t.Fatal("unknown WS action or echo")
	}
}
func TestOneBotMessageIDRoutesDeleteAndCache(t *testing.T) {
	s, b := newOBTest(t)
	ids := []int32{}
	for _, params := range []string{`{"group_id":90001,"message":"a"}`, `{"group_id":90002,"message":"b"}`, `{"user_id":123,"message":"c"}`} {
		r := s.handleOneBotAction(context.Background(), obAction{Action: "send_msg", Params: json.RawMessage(params)})
		if r.Status != "ok" {
			t.Fatal(r)
		}
		id := r.Data.(map[string]any)["message_id"].(int32)
		ids = append(ids, id)
		g := s.handleOneBotAction(context.Background(), obAction{Action: "get_msg", Params: json.RawMessage(fmt.Sprintf(`{"message_id":%d}`, id))})
		if g.Status != "ok" {
			t.Fatal(g)
		}
	}
	if ids[0] == ids[1] || ids[1] == ids[2] {
		t.Fatal("message sequence collisions not isolated")
	}
	for _, id := range ids {
		r := s.handleOneBotAction(context.Background(), obAction{Action: "delete_msg", Params: json.RawMessage(fmt.Sprintf(`{"message_id":%d}`, id))})
		if r.Status != "ok" {
			t.Fatal(r)
		}
	}
	if len(b.recalls) != 3 || b.recalls[0].Group != 90001 || b.recalls[1].Group != 90002 || b.recalls[2].Uin != 123 {
		t.Fatal("recall target mismatch", b.recalls)
	}
	other, _ := newOBTest(t)
	r := other.handleOneBotAction(context.Background(), obAction{Action: "delete_msg", Params: json.RawMessage(fmt.Sprintf(`{"message_id":%d}`, ids[0]))})
	if r.Status != "failed" {
		t.Fatal("foreign account message accepted")
	}
}
func TestOneBotWSRolesEchoAndPing(t *testing.T) {
	s, b := newOBTest(t)
	server := httptest.NewServer(s)
	defer server.Close()
	for _, path := range []string{"/api", "/event", "/onebot/v11/"} {
		c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		if path != "/api" {
			var life map[string]any
			if c.ReadJSON(&life) != nil || life["sub_type"] != "connect" {
				t.Fatal("missing lifecycle")
			}
		}
		if path == "/event" {
			b.events <- protocol.MessageEvent{SelfUin: 10001, Msg: &protocol.IncomingMessage{SenderUin: 123, MsgSeq: 8, MsgTime: 100, Content: "hello"}}
			var event map[string]any
			if err = c.ReadJSON(&event); err != nil || event["post_type"] != "message" {
				t.Fatal("event not delivered", err)
			}
			if _, ok := event["message"].([]any); !ok {
				t.Fatal("array format not honored")
			}
		} else {
			if err = c.WriteJSON(map[string]any{"action": "get_login_info", "echo": map[string]any{"x": []int{1, 2}}}); err != nil {
				t.Fatal(err)
			}
			var response map[string]any
			if err = c.ReadJSON(&response); err != nil || response["status"] != "ok" {
				t.Fatal("response missing", err)
			}
			if response["echo"].(map[string]any)["x"].([]any)[1] != float64(2) {
				t.Fatal("echo corrupted")
			}
		}
		c.Close()
	}
}
func TestOneBotHTTPPostSignatureAndQuickReply(t *testing.T) {
	s, b := newOBTest(t)
	got := make(chan bool, 8)
	secret := "post-test-secret"
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		h := hmac.New(sha1.New, []byte(secret))
		h.Write(raw)
		valid := r.Header.Get("X-Signature") == "sha1="+hex.EncodeToString(h.Sum(nil)) && r.Header.Get("X-Self-ID") == "10001"
		var v map[string]any
		json.Unmarshal(raw, &v)
		if v["post_type"] == "message" {
			got <- valid
			w.Write([]byte(`{"reply":"received","auto_escape":true}`))
		} else {
			w.WriteHeader(204)
		}
	}))
	defer target.Close()
	if err := s.ConfigureOneBot(OneBotConfig{HTTPPost: []OneBotPost{{URL: target.URL, Secret: secret}}}); err != nil {
		t.Fatal(err)
	}
	b.events <- protocol.MessageEvent{SelfUin: 10001, Msg: &protocol.IncomingMessage{SenderUin: 123, MsgSeq: 19, MsgTime: 100, Content: "test"}}
	select {
	case valid := <-got:
		if !valid {
			t.Fatal("invalid event signature")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no POST")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		b.mu.Lock()
		sent := len(b.sent) > 0
		b.mu.Unlock()
		if sent {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.sent) != 1 || b.sent[0] != "received" || b.targets[0].Uin != 123 {
		t.Fatal("quick reply wrong account/target", b.sent)
	}
}
func TestOneBotReverseWSHeadersActionsAndReconnect(t *testing.T) {
	s, _ := newOBTest(t)
	seen := make(chan bool, 8)
	up := websocket.Upgrader{}
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		valid := r.Header.Get("X-Self-ID") == "10001" && r.Header.Get("X-Client-Role") == "Universal" && r.Header.Get("Authorization") == "Bearer reverse-token"
		c, e := up.Upgrade(w, r, nil)
		if e != nil {
			return
		}
		defer c.Close()
		var life map[string]any
		if c.ReadJSON(&life) != nil {
			return
		}
		c.WriteJSON(map[string]any{"action": "get_status", "echo": 7})
		var resp map[string]any
		if c.ReadJSON(&resp) != nil {
			return
		}
		seen <- valid && resp["status"] == "ok" && resp["echo"] == float64(7)
	}))
	defer target.Close()
	u, _ := url.Parse(target.URL)
	u.Scheme = "ws"
	if e := s.ConfigureOneBot(OneBotConfig{ReverseWS: []OneBotReverse{{URL: u.String(), Role: "Universal", Token: "reverse-token", ReconnectInterval: 500}}}); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 2; i++ {
		select {
		case valid := <-seen:
			if !valid {
				t.Fatal("reverse headers/action failed")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("reverse did not connect/reconnect")
		}
	}
	if e := s.ConfigureOneBot(OneBotConfig{}); e != nil {
		t.Fatal(e)
	}
}
