package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"openopq/internal/bot"
	"openopq/internal/protocol"
)

type testBot12 struct {
	*obRichTestBot
	uin uint32
}

func (b *testBot12) Self(context.Context) (bot.Self, error) {
	return bot.Self{Uin: b.uin, Nickname: "v12-test", Online: true}, nil
}
func newTest12(t *testing.T, uin uint32) (*Server, *testBot12) {
	t.Helper()
	b := &testBot12{obRichTestBot: &obRichTestBot{obTestBot: &obTestBot{Stub: bot.NewStub(), events: make(chan protocol.MessageEvent, 128)}}, uin: uin}
	s := NewServer(b, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{OneBot12: true})
	t.Cleanup(func() { s.Close() })
	return s, b
}
func request12(t *testing.T, s *Server, request string) ob12Response {
	t.Helper()
	w := obHTTP(s, "POST", "/onebot/v12/", request, "application/json")
	if w.Code != 200 {
		t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
	}
	var out ob12Response
	if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil {
		t.Fatal(e)
	}
	return out
}
func TestV12HTTPFormatAndIdentity(t *testing.T) {
	s, _ := newTest12(t, 3912749521)
	for _, v := range []struct {
		request string
		code    int
	}{{`{"action":"get_self_info","params":{}}`, 0}, {`{"action":"get_self_info"}`, 10001}, {`{"action":"get_status","params":{},"echo":{}}`, 10001}, {`{"action":"get_status","params":{},"echo":null}`, 10001}, {`{"action":"get_status","params":[]}`, 10001}, {`{"action":"none","params":{}}`, 10002}, {`{"action":"get_self_info","params":{},"self":{"platform":"qq","user_id":"other"}}`, 10102}, {`{"action":"get_group_info","params":{"group_id":90001}}`, 10003}, {`{"action":"send_message","params":{"detail_type":"private","user_id":"123","message":"text"}}`, 10003}, {`{"action":"send_message","params":{"detail_type":"private","user_id":"123","message":[{"type":"unknown","data":{}}]}}`, 10005}} {
		r := request12(t, s, v.request)
		if r.Retcode != v.code {
			t.Fatal(v.request, r)
		}
	}
	r := request12(t, s, `{"action":"get_self_info","params":{},"echo":"exact"}`)
	m := map12(r.Data)
	if m["user_id"] != "3912749521" || m["user_name"] != "v12-test" || r.Echo != "exact" || r.Message != "" {
		t.Fatal(r)
	}
	for _, v := range []struct {
		method, path, ct string
		status           int
	}{{"GET", "/onebot/v12/", "", 405}, {"POST", "/onebot/v12/", "application/msgpack", 415}, {"POST", "/onebot/v12/bad", "application/json", 404}} {
		w := obHTTP(s, v.method, v.path, `{}`, v.ct)
		if w.Code != v.status {
			t.Fatal(w.Code, v)
		}
	}
}

func TestV12SelfSentMessageSurvivesV11EventClassification(t *testing.T) {
	s, _ := newTest12(t, 10001)
	s.v12.ingest(onebotEventJSON(protocol.MessageEvent{SelfUin: 10001, Msg: &protocol.IncomingMessage{SenderUin: 10001, ToUin: 123, MsgSeq: 7, MsgTime: 1234, Content: "sent"}}))
	s.v12.mu.Lock()
	events := append([]json.RawMessage(nil), s.v12.events...)
	s.v12.mu.Unlock()
	for _, raw := range events {
		var event map[string]any
		_ = json.Unmarshal(raw, &event)
		if event["type"] == "message" && event["user_id"] == "10001" {
			if _, exists := event["post_type"]; exists {
				t.Fatal("v11 envelope leaked into v12", event)
			}
			return
		}
	}
	t.Fatal("self-sent message disappeared from the v12 adapter")
}
func TestV12MultiAccountHTTPAndWebSocket(t *testing.T) {
	a, ba := newTest12(t, 10001)
	b, bb := newTest12(t, 10002)
	gateway := NewOneBot12Gateway(func(context.Context) []*Server { return []*Server{a, b} }, nil, OneBot12Config{})
	defer gateway.Close()
	r := request12(t, gateway, `{"action":"get_self_info","params":{}}`)
	if r.Retcode != 10101 {
		t.Fatal(r)
	}
	r = request12(t, gateway, `{"action":"get_self_info","params":{},"self":{"platform":"qq","user_id":"10002"}}`)
	if map12(r.Data)["user_id"] != "10002" {
		t.Fatal(r)
	}
	server := httptest.NewServer(gateway)
	defer server.Close()
	c, _, e := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/onebot/v12/", nil)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	var event map[string]any
	if e = c.ReadJSON(&event); e != nil || event["detail_type"] != "connect" {
		t.Fatal(event, e)
	}
	if e = c.ReadJSON(&event); e != nil || event["detail_type"] != "status_update" {
		t.Fatal(event, e)
	}
	// The gateway subscribes dynamically, without taking ownership of the QQ connections.
	time.Sleep(250 * time.Millisecond)
	for _, v := range []struct {
		bot  *testBot12
		self uint32
	}{{ba, 10001}, {bb, 10002}} {
		v.bot.events <- protocol.MessageEvent{SelfUin: v.self, Msg: &protocol.IncomingMessage{SenderUin: 123, MsgSeq: v.self, MsgTime: 12345, Content: "test"}}
	}
	owners := map[string]bool{}
	for len(owners) < 2 {
		if e = c.ReadJSON(&event); e != nil {
			t.Fatal(e)
		}
		if event["type"] == "message" {
			self := event["self"].(map[string]any)
			owners[self["user_id"].(string)] = true
			if _, ok := event["message_id"].(string); !ok {
				t.Fatal("numeric message ID")
			}
		}
	}
	_ = c.WriteMessage(websocket.TextMessage, []byte(`{"action":"get_self_info","params":{},"self":{"platform":"qq","user_id":"10001"},"echo":"account-a"}`))
	for {
		var v map[string]any
		if e = c.ReadJSON(&v); e != nil {
			t.Fatal(e)
		}
		if v["echo"] == "account-a" {
			if v["status"] != "ok" || v["data"].(map[string]any)["user_id"] != "10001" {
				t.Fatal(v)
			}
			break
		}
	}
	// Delivery to WS never consumes HTTP polling's independent event buffer.
	r = request12(t, gateway, `{"action":"get_latest_events","params":{"limit":20,"timeout":0}}`)
	if len(r.Data.([]any)) != 2 {
		t.Fatal(r)
	}
}
func TestV12FileUploadFragmentsAndIsolation(t *testing.T) {
	s, _ := newTest12(t, 10001)
	other, _ := newTest12(t, 10002)
	data := []byte("abcde")
	h := sha256.Sum256(data)
	sum := hex.EncodeToString(h[:])
	r := request12(t, s, `{"action":"upload_file_fragmented","params":{"stage":"prepare","name":"test.txt","total_size":5}}`)
	id := map12(r.Data)["file_id"].(string)
	transfer := func(offset int, chunk string) ob12Response {
		return request12(t, s, fmt.Sprintf(`{"action":"upload_file_fragmented","params":{"stage":"transfer","file_id":%q,"offset":%d,"data":%q}}`, id, offset, base64.StdEncoding.EncodeToString([]byte(chunk))))
	}
	if transfer(2, "cde").Retcode != 0 {
		t.Fatal("valid fragment failed")
	}
	finish := fmt.Sprintf(`{"action":"upload_file_fragmented","params":{"stage":"finish","file_id":%q,"sha256":%q}}`, id, sum)
	if request12(t, s, finish).Retcode == 0 {
		t.Fatal("upload with gaps accepted")
	}
	if transfer(2, "xxx").Retcode == 0 {
		t.Fatal("conflicting overlap accepted")
	}
	if transfer(0, "abc").Retcode != 0 {
		t.Fatal("valid overlap failed")
	}
	if r = request12(t, s, finish); r.Retcode != 0 {
		t.Fatal(r)
	}
	get := fmt.Sprintf(`{"action":"get_file","params":{"file_id":%q,"type":"data"}}`, id)
	if r = request12(t, s, get); map12(r.Data)["data"] != base64.StdEncoding.EncodeToString(data) {
		t.Fatal(r)
	}
	if request12(t, other, get).Retcode == 0 {
		t.Fatal("file ID crossed accounts")
	}
	r = request12(t, s, fmt.Sprintf(`{"action":"get_file_fragmented","params":{"stage":"transfer","file_id":%q,"offset":1,"size":2}}`, id))
	if map12(r.Data)["data"] != base64.StdEncoding.EncodeToString([]byte("bc")) {
		t.Fatal(r)
	}
	r = request12(t, s, `{"action":"upload_file","params":{"type":"data","name":"test","data":"eA==","sha256":"bad"}}`)
	if r.Retcode != 10003 {
		t.Fatal(r)
	}
	r = request12(t, s, `{"action":"upload_file","params":{"type":"data","name":"test","data":"eA=="}}`)
	uploadID := map12(r.Data)["file_id"].(string)
	r = request12(t, s, fmt.Sprintf(`{"action":"send_message","params":{"detail_type":"private","user_id":"123","message":[{"type":"text","data":{"text":"before"}},{"type":"image","data":{"file_id":%q}},{"type":"text","data":{"text":"after"}}]}}`, uploadID))
	if r.Retcode != 0 {
		t.Fatal(r)
	}
	if _, ok := map12(r.Data)["message_id"].(string); !ok {
		t.Fatal("send response ID must be string")
	}
}
func TestV12WebhookHeadersAndReturnedActions(t *testing.T) {
	s, _ := newTest12(t, 10001)
	received := make(chan bool, 8)
	webhook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer exact-token" || r.Header.Get("X-OneBot-Version") != "12" || r.Header.Get("X-Impl") != "openopq" {
			t.Error("incorrect v12 headers")
		}
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte(`"type":"message"`)) {
			received <- true
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"action":"get_status","params":{}}]`))
	}))
	defer webhook.Close()
	c, _ := s.OneBotSnapshot()
	c.V12.Webhooks = []OneBot12Webhook{{URL: webhook.URL, Token: "exact-token", Timeout: 1000}}
	if e := s.ConfigureOneBot(c); e != nil {
		t.Fatal(e)
	}
	s.v12.ingest(onebotEventJSON(protocol.MessageEvent{SelfUin: 10001, Msg: &protocol.IncomingMessage{SenderUin: 123, Content: "test", MsgSeq: 7}}))
	select {
	case <-received:
	case <-time.After(2 * time.Second):
		t.Fatal("webhook did not receive event")
	}
}

func TestV12ReverseWSHeadersConnectEchoAndReconnect(t *testing.T) {
	s, _ := newTest12(t, 10001)
	accepted := make(chan bool, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer reverse-exact" || r.Header.Get("Sec-WebSocket-Protocol") != "12.openopq" {
			t.Error("incorrect reverse headers")
		}
		up := websocket.Upgrader{Subprotocols: []string{"12.openopq"}, CheckOrigin: func(*http.Request) bool { return true }}
		c, e := up.Upgrade(w, r, nil)
		if e != nil {
			return
		}
		defer c.Close()
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		var first map[string]any
		if e = c.ReadJSON(&first); e != nil || first["detail_type"] != "connect" {
			t.Error("connect was not first", e)
			return
		}
		_ = c.WriteMessage(websocket.TextMessage, []byte(`{"action":"get_version","params":{},"echo":"reverse-echo"}`))
		for {
			var v map[string]any
			if e = c.ReadJSON(&v); e != nil {
				return
			}
			if v["echo"] == "reverse-echo" {
				if v["status"] != "ok" {
					t.Error(v)
				}
				accepted <- true
				return
			}
		}
	}))
	defer server.Close()
	config, _ := s.OneBotSnapshot()
	config.V12.ReverseWS = []OneBot12Reverse{{URL: "ws" + strings.TrimPrefix(server.URL, "http"), Token: "reverse-exact", Interval: 20}}
	if e := s.ConfigureOneBot(config); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-accepted:
		case <-time.After(3 * time.Second):
			t.Fatal("reverse connection did not reconnect and respond")
		}
	}
	config.V12.ReverseWS = nil
	if e := s.ConfigureOneBot(config); e != nil {
		t.Fatal(e)
	}
}
