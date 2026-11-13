package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"openopq/internal/bot"
	"openopq/internal/protocol"
)

// Independent wire fixtures, checked against the public v11 schema exposed by
// SnowLuma v2.1.1. No reference implementation or reference tests are embedded.
func assertOBJSON(t *testing.T, actual []byte, expected string) {
	t.Helper()
	var a, b any
	if err := json.Unmarshal(actual, &a); err != nil {
		t.Fatal(err, string(actual))
	}
	if err := json.Unmarshal([]byte(expected), &b); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("wire mismatch\ngot:  %s\nwant: %s", actual, expected)
	}
}

func TestV11ResponseEnvelopeContract(t *testing.T) {
	s, _ := newOBTest(t)
	for _, c := range []struct{ request, expected string }{
		{`{"action":"absent"}`, `{"status":"failed","retcode":1404,"data":null,"wording":"unknown action"}`},
		{`{"action":"absent","echo":null}`, `{"status":"failed","retcode":1404,"data":null,"wording":"unknown action","echo":null}`},
		{`{"action":"get_login_info","echo":{"n":[1,"x",false]}}`, `{"status":"ok","retcode":0,"data":{"user_id":10001,"nickname":"test"},"echo":{"n":[1,"x",false]}}`},
		{`{"action":"get_login_info","params":[]}`, `{"status":"failed","retcode":1400,"data":null,"wording":"params must be an object"}`},
	} {
		w := obHTTP(s, "POST", "/onebot/v11/", c.request, "application/json")
		if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Type"), "application/json") {
			t.Fatal(w.Code, w.Body.String())
		}
		assertOBJSON(t, w.Body.Bytes(), c.expected)
	}
	for _, response := range []obResponse{obFail("QQ refused", nil), obUnsupported("feature", nil)} {
		b, _ := json.Marshal(response)
		var v map[string]any
		_ = json.Unmarshal(b, &v)
		if len(v) != 4 || v["retcode"] != float64(100) || v["data"] != nil || v["wording"] == "" {
			t.Fatal(string(b))
		}
	}
	assertOBJSON(t, obHTTP(s, "POST", "/get_status", "{", "application/json").Body.Bytes(), `{"status":"failed","retcode":1400,"data":null,"wording":"bad request: invalid json"}`)
}

func TestV11HTTPFormsQueriesAndEchoContract(t *testing.T) {
	s, b := newOBTest(t)
	message := `[{"type":"text","data":{"text":"first"}},{"type":"at","data":{"qq":"456"}},{"type":"text","data":{"text":"last"}}]`
	for _, c := range []struct{ method, path, body, ct string }{
		{"POST", "/onebot/v11/", `{"action":"send_private_msg","user_id":123,"message":` + message + `,"echo":null}`, "application/json"},
		{"POST", "/send_private_msg", `{"params":{"user_id":"123","message":` + message + `},"echo":null}`, "application/json"},
		{"POST", "/onebot/v11/", "action=send_private_msg&user_id=123&message=" + url.QueryEscape(message) + "&echo=null", "application/x-www-form-urlencoded"},
		{"POST", "/onebot/v11/", "action=send_private_msg&user_id=123&message=" + url.QueryEscape(message) + "&echo=null", ""},
		{"GET", "/send_private_msg?user_id=123&message=" + url.QueryEscape(message) + "&echo=null", "", ""},
	} {
		w := obHTTP(s, c.method, c.path, c.body, c.ct)
		var response map[string]any
		if json.Unmarshal(w.Body.Bytes(), &response) != nil || response["status"] != "ok" {
			t.Fatal(c, w.Body.String())
		}
		if echo, exists := response["echo"]; !exists || echo != nil {
			t.Fatal("lost explicit null echo", response)
		}
	}
	if len(b.sent) != 5 {
		t.Fatal("encodings did not dispatch", b.sent)
	}
	for _, target := range b.targets {
		if target.Uin != 123 {
			t.Fatal(target)
		}
	}
	assertOBJSON(t, obHTTP(s, "GET", "/onebot/v11/", "", "").Body.Bytes(), `{"status":"ok","retcode":0,"data":{"online":true}}`)
}

func TestV11MessageAndGetMsgContract(t *testing.T) {
	s, _ := newOBTest(t)
	m := &protocol.IncomingMessage{IsGroup: true, GroupUin: 90001, GroupName: "Garden", SenderUin: 123, SenderNick: "April", MsgSeq: 77, MsgTime: 1234,
		Segments: []protocol.Segment{{Type: "text", Data: map[string]string{"text": "left"}}, {Type: "at", Data: map[string]string{"qq": "456"}}, {Type: "face", Data: map[string]string{"id": "14"}}, {Type: "text", Data: map[string]string{"text": "right"}}}}
	var event map[string]any
	_ = json.Unmarshal(s.ob.event(protocol.MessageEvent{SelfUin: 10001, Msg: m}), &event)
	id := int32(event["message_id"].(float64))
	event["message_id"] = 42 // The contract fixes the ID type, not another implementation's opaque ID algorithm.
	encoded, _ := json.Marshal(event)
	assertOBJSON(t, encoded, `{"time":1234,"self_id":10001,"post_type":"message","message_type":"group","sub_type":"normal","message_id":42,"message_seq":77,"group_id":90001,"group_name":"Garden","user_id":123,"message":[{"type":"text","data":{"text":"left"}},{"type":"at","data":{"qq":"456"}},{"type":"face","data":{"id":"14"}},{"type":"text","data":{"text":"right"}}],"raw_message":"left[CQ:at,qq=456][CQ:face,id=14]right","font":0,"sender":{"user_id":123,"nickname":"April","card":"","role":"member","sex":"unknown","age":0},"anonymous":null}`)
	response := s.handleOneBotAction(context.Background(), obAction{Action: "get_msg", Params: json.RawMessage(fmt.Sprintf(`{"message_id":%d}`, id))})
	data := response.Data.(map[string]any)
	event["message_id"] = id
	delete(event, "self_id")
	delete(event, "post_type")
	event["real_id"] = id
	a, _ := json.Marshal(data)
	want, _ := json.Marshal(event)
	assertOBJSON(t, a, string(want))
	if err := s.ConfigureOneBot(OneBotConfig{MessageFormat: "string"}); err != nil {
		t.Fatal(err)
	}
	response = s.handleOneBotAction(context.Background(), obAction{Action: "get_msg", Params: json.RawMessage(fmt.Sprintf(`{"message_id":%d}`, id))})
	if response.Data.(map[string]any)["message"] != "left[CQ:at,qq=456][CQ:face,id=14]right" {
		t.Fatal(response)
	}
}

func TestV11SentEchoKeepsReceiptAndID(t *testing.T) {
	s, _ := newOBTest(t)
	ctx := context.Background()
	result := protocol.SendResult{Sequence: 8, Timestamp: 100, Random: 71, ClientSequence: 99}
	id := s.ob.rememberSend(ctx, protocol.SendTarget{Uin: 123}, result, "hello")
	var event map[string]any
	_ = json.Unmarshal(s.ob.event(protocol.MessageEvent{SelfUin: 10001, Msg: &protocol.IncomingMessage{SenderUin: 10001, ToUin: 123, MsgSeq: 8, MsgTime: 100, Content: "hello"}}), &event)
	if event["post_type"] != "message_sent" || event["target_id"] != float64(123) || event["message_id"] != float64(id) {
		t.Fatal(event)
	}
	s.ob.mu.Lock()
	cached := s.ob.cache[id]
	s.ob.mu.Unlock()
	if !cached.Sent || cached.Result.Random != 71 || cached.Result.ClientSequence != 99 {
		t.Fatal("echo lost recall receipt", cached)
	}
	var private map[string]any
	_ = json.Unmarshal(onebotEventJSON(protocol.MessageEvent{SelfUin: 10001, Msg: &protocol.IncomingMessage{SenderUin: 123, MsgTime: 100}}), &private)
	sender := private["sender"].(map[string]any)
	if _, exists := sender["card"]; exists {
		t.Fatal("private sender leaked group fields", private)
	}
}

type contractStatusBot struct{ *obTestBot }

func (*contractStatusBot) HeartbeatStatus() bot.HeartbeatStatus {
	return bot.HeartbeatStatus{Stale: true}
}

func TestV11StatusAndGroupInfoContract(t *testing.T) {
	_, base := newOBTest(t)
	s := NewServer(&contractStatusBot{base}, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{OneBot: true})
	defer s.Close()
	s.ob.startedAt = time.Now().Add(-9 * time.Second)
	status := s.ob.statusData(context.Background())
	if status["online"] != true || status["good"] != false || status["time"] != int64(9) {
		t.Fatal(status)
	}
	g := bot.Group{GroupUin: 90001, GroupName: "Garden", MemberCount: 12, MaxMemberCount: 200, CreateTime: 123, Remark: "local", Memo: "news", Description: "about", ShutUpAllTimestamp: ^uint32(0)}
	b, _ := json.Marshal(oneBotGroupInfo(g))
	assertOBJSON(t, b, `{"group_id":90001,"group_name":"Garden","group_remark":"local","member_count":12,"max_member_count":200,"group_create_time":123,"group_level":0,"group_memo":"news","group_description":"about","group_all_shut":-1}`)
	b, _ = json.Marshal(g)
	assertOBJSON(t, b, `{"GroupUin":90001,"GroupName":"Garden","MemberCount":12,"MaxMemberCount":200}`)
	g.ShutUpAllTimestamp = 1
	if oneBotGroupInfo(g)["group_all_shut"] != 0 {
		t.Fatal("expired group mute still reported active")
	}
}

func TestV11NoticeContract(t *testing.T) {
	for _, c := range []struct {
		n     protocol.Notice
		extra string
	}{
		{protocol.Notice{Kind: protocol.NoticeGroupJoin, GroupUin: 90001, UserUin: 123, OperatorUin: 456, IncreaseType: 131}, `"notice_type":"group_increase","sub_type":"invite","group_id":90001,"user_id":123,"operator_id":456`},
		{protocol.Notice{Kind: protocol.NoticeGroupJoin, GroupUin: 90001, UserUin: 123, OperatorUin: 456, IncreaseType: 130}, `"notice_type":"group_increase","sub_type":"approve","group_id":90001,"user_id":123,"operator_id":456`},
		{protocol.Notice{Kind: protocol.NoticeGroupLeave, GroupUin: 90001, UserUin: 10001, OperatorUin: 456, ExitType: 129}, `"notice_type":"group_decrease","sub_type":"disband","group_id":90001,"user_id":10001,"operator_id":456`},
		{protocol.Notice{Kind: protocol.NoticeGroupLeave, GroupUin: 90001, UserUin: 10001, OperatorUin: 456, ExitType: 131}, `"notice_type":"group_decrease","sub_type":"kick_me","group_id":90001,"user_id":10001,"operator_id":456`},
		{protocol.Notice{Kind: protocol.NoticeGroupLeave, GroupUin: 90001, UserUin: 123, OperatorUin: 456, ExitType: 3}, `"notice_type":"group_decrease","sub_type":"kick","group_id":90001,"user_id":123,"operator_id":456`},
		{protocol.Notice{Kind: protocol.NoticeFriendPoke, UserUin: 10001, Target: 123, Action: "poke", Suffix: "gently", ActionImageURL: "https://example.test/poke.png"}, `"notice_type":"notify","sub_type":"poke","user_id":123,"sender_id":10001,"target_id":123,"action":"poke","suffix":"gently","action_img_url":"https://example.test/poke.png"`},
	} {
		c.n.MsgTime = 1234
		assertOBJSON(t, onebotNoticeJSON(protocol.MessageEvent{SelfUin: 10001, Notice: &c.n}), `{"time":1234,"self_id":10001,"post_type":"notice",`+c.extra+`}`)
	}
}

func TestV11WebSocketFailureMatchesHTTP(t *testing.T) {
	s, _ := newOBTest(t)
	server := httptest.NewServer(s)
	defer server.Close()
	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/onebot/v11/api", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	request := `{"action":"absent","echo":{"id":7}}`
	if err := c.WriteMessage(websocket.TextMessage, []byte(request)); err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, response, err := c.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	w := obHTTP(s, "POST", "/onebot/v11/", request, "application/json")
	assertOBJSON(t, response, w.Body.String())
}

type contractForwardBot struct {
	*obTestBot
	lastID string
}

func (b *contractForwardBot) OneBotAction(_ context.Context, action string, raw json.RawMessage) (any, error) {
	var p struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(raw, &p)
	b.lastID = p.ID
	return []*protocol.IncomingMessage{{SenderUin: 321, SenderNick: "May", MsgSeq: 17, MsgTime: 1234, Content: "forwarded"}}, nil
}
func TestV11ForwardMessagesAndCachedAlias(t *testing.T) {
	_, base := newOBTest(t)
	b := &contractForwardBot{obTestBot: base}
	s := NewServer(b, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{OneBot: true})
	defer s.Close()
	var incoming map[string]any
	_ = json.Unmarshal(s.ob.event(protocol.MessageEvent{SelfUin: 10001, Msg: &protocol.IncomingMessage{SenderUin: 123, MsgSeq: 8, MsgTime: 1234, Segments: []protocol.Segment{{Type: "forward", Data: map[string]string{"id": "opaque-forward"}}}}}), &incoming)
	response := s.handleOneBotAction(context.Background(), obAction{Action: "get_forward_msg", Params: json.RawMessage(fmt.Sprintf(`{"message_id":%.0f}`, incoming["message_id"]))})
	if response.Status != "ok" || b.lastID != "opaque-forward" {
		t.Fatal(response, b.lastID)
	}
	data := response.Data.(map[string]any)
	messages := data["messages"].([]map[string]any)
	if len(data) != 2 || len(data["message"].([]map[string]any)) != len(messages) || len(messages) != 1 || messages[0]["message_seq"] != float64(17) || messages[0]["post_type"] != "message" {
		t.Fatal(data)
	}
	if _, ok := messages[0]["message"].([]any); !ok {
		t.Fatal("forward returned send-only node shape", data)
	}
	response = s.handleOneBotAction(context.Background(), obAction{Action: "get_forward_msg", Params: json.RawMessage(`{"message_id":"opaque-alias"}`)})
	if response.Status != "ok" || b.lastID != "opaque-alias" {
		t.Fatal("opaque message_id alias was treated as a numeric QQ sequence", response, b.lastID)
	}
}
