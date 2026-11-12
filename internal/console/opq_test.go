package console

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"openopq/internal/bot"
	"openopq/internal/protocol"
)

type opqTestBot struct{ *testBot }

func (b *opqTestBot) Groups(context.Context) ([]bot.Group, error) {
	return []bot.Group{{GroupUin: b.uin + 100, GroupName: fmt.Sprint(b.uin), MemberCount: 2}}, nil
}
func (b *opqTestBot) SendMessage(context.Context, protocol.SendTarget, string, []protocol.SendAt) (protocol.SendResult, error) {
	return protocol.SendResult{Sequence: b.uin}, nil
}

func TestOPQSinglePortUsesQQAndCGIWithoutDeviceSelectors(t *testing.T) {
	h := testHub(t)
	h.options.Factory = func(_ []byte, uin uint32) (ManagedBot, error) { return &opqTestBot{&testBot{uin: uin}}, nil }
	ids := map[string]string{}
	for _, account := range []string{"123456", "234567"} {
		id, err := h.Add(account)
		if err != nil {
			t.Fatal(err)
		}
		ids[account] = id
		if err = h.BeginAccount(account); err != nil {
			t.Fatal(err)
		}
		waitPhase(t, h.find(id).server, "online")
	}
	for _, account := range []string{"123456", "234567", "123456"} {
		w := hubCall(h, "POST", "/v1/LuaApiCaller?funcname=MagicCgiCmd&timeout=10&qq="+account, `{"CgiCmd":"GetGroupLists","CgiRequest":{}}`)
		var groups struct {
			CgiBaseResponse struct{ Ret int }
			ResponseData    struct{ GroupLists []struct{ GroupName string } }
		}
		if err := json.Unmarshal(w.Body.Bytes(), &groups); err != nil {
			t.Fatal(err)
		}
		if groups.CgiBaseResponse.Ret != 0 || len(groups.ResponseData.GroupLists) != 1 || groups.ResponseData.GroupLists[0].GroupName != account {
			t.Fatalf("wrong account: %s", w.Body.String())
		}
		w = hubCall(h, "POST", "/v1/LuaApiCaller?funcname=MagicCgiCmd&qq="+account, `{"CgiCmd":"MessageSvc.PbSendMsg","CgiRequest":{"ToUin":234567,"ToType":1,"Content":"test"}}`)
		if !strings.Contains(w.Body.String(), `"MsgSeq":`+account) {
			t.Fatalf("sender selected from recipient: %s", w.Body.String())
		}
	}
	for _, selector := range []string{"", "&qq=", "&qq=0", "&qq=not-a-qq", "&qq=999999", "&qq=123456&qq=234567", "&qq=123456&session=" + ids["234567"]} {
		w := hubCall(h, "POST", "/v1/LuaApiCaller?funcname=MagicCgiCmd"+selector, `{"CgiCmd":"GetGroupLists","CgiRequest":{}}`)
		if !strings.Contains(w.Body.String(), `"Ret":-1`) {
			t.Fatalf("unsafe selector %q: %s", selector, w.Body.String())
		}
	}
	for _, path := range []string{"/v1/clusterinfo", "/v1/LuaApiCaller?funcname=MagicCgiCmd&qq=123456"} {
		method, body := "GET", ""
		if strings.Contains(path, "LuaApiCaller") {
			method, body = "POST", `{"CgiCmd":"ClusterInfo","CgiRequest":{}}`
		}
		w := hubCall(h, method, path, body)
		if !strings.Contains(w.Body.String(), `"QQUsersCounts":2`) || !strings.Contains(w.Body.String(), `"QQ":"123456"`) || !strings.Contains(w.Body.String(), `"QQ":"234567"`) || strings.Contains(strings.ToLower(w.Body.String()), "guid") {
			t.Fatalf("wrong framework response: %s", w.Body.String())
		}
	}
}

func TestOPQUnknownCGIDoesNotFallThroughToSend(t *testing.T) {
	h := testHub(t)
	id, err := h.Add("123456")
	if err != nil {
		t.Fatal(err)
	}
	if err = h.BeginAccount("123456"); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, h.find(id).server, "online")
	w := hubCall(h, "POST", "/v1/LuaApiCaller?qq=123456&funcname=MagicCgiCmd", `{"CgiCmd":"GetFriendsList","CgiRequest":{}}`)
	if !strings.Contains(w.Body.String(), "unsupported CgiCmd: GetFriendsList") || strings.Contains(w.Body.String(), "Content is empty") {
		t.Fatal(w.Body.String())
	}
}

func TestOPQUnsupportedMessagesAreNotSilentlySentAsText(t *testing.T) {
	h := testHub(t)
	h.options.Factory = func(_ []byte, uin uint32) (ManagedBot, error) { return &opqTestBot{&testBot{uin: uin}}, nil }
	id, err := h.Add("123456")
	if err != nil {
		t.Fatal(err)
	}
	if err = h.BeginAccount("123456"); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, h.find(id).server, "online")
	for _, fields := range []string{
		`"ToType":3`,
		`"ToType":2,"Images":[{"FileId":123}]`,
		`"ToType":2,"ReplyTo":{"MsgSeq":123}`,
		`"ToType":1,"Voice":{"FileToken":"test"}`,
	} {
		w := hubCall(h, "POST", "/v1/LuaApiCaller?qq=123456&funcname=MagicCgiCmd", `{"CgiCmd":"MessageSvc.PbSendMsg","CgiRequest":{"ToUin":234567,"Content":"test",`+fields+`}}`)
		if !strings.Contains(w.Body.String(), `"Ret":-1`) || strings.Contains(w.Body.String(), "MsgSeq") {
			t.Fatal(w.Body.String())
		}
	}
}

func TestAttachedOPQAccountQueriesPreserveSourceSessions(t *testing.T) {
	source := testHub(t)
	source.options.Factory = func(_ []byte, uin uint32) (ManagedBot, error) { return &opqTestBot{&testBot{uin: uin}}, nil }
	server := httptest.NewServer(source)
	defer server.Close()
	h := testHub(t)
	for _, account := range []string{"123456", "234567"} {
		id, err := source.Add(account)
		if err != nil {
			t.Fatal(err)
		}
		if err = source.BeginAccount(account); err != nil {
			t.Fatal(err)
		}
		waitPhase(t, source.find(id).server, "online")
		if err = h.Attach(server.URL + "/accounts/" + id); err != nil {
			t.Fatal(err)
		}
	}
	w := hubCall(h, "POST", "/v1/LuaApiCaller?qq=234567&funcname=MagicCgiCmd", `{"CgiCmd":"GetGroupLists","CgiRequest":{}}`)
	if !strings.Contains(w.Body.String(), `"GroupCode":234667`) {
		t.Fatal(w.Body.String())
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	for _, s := range source.states() {
		if !s.Online {
			t.Fatal("compatibility hub closed an attached source account")
		}
	}
}
