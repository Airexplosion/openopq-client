package client

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"openopq/internal/protocol"
)

type obQueryExchange func(context.Context, string, []byte) ([]byte, error)

func (f obQueryExchange) Exchange(c context.Context, cmd string, b []byte) ([]byte, error) {
	return f(c, cmd, b)
}
func obFixtureReply(cmd string, body []byte) []byte {
	c, _ := protocol.ParseOIDBCommand(cmd)
	return protocol.BuildOIDB(protocol.OIDBRequest{Command: c, Body: body})
}
func TestOneBotFriendPaginationLearnsUIDAndStopsCycles(t *testing.T) {
	calls := 0
	backend := NewOneBotQueryBackend(obQueryExchange(func(ctx context.Context, cmd string, data []byte) ([]byte, error) {
		if cmd != protocol.FetchFriendsCommand {
			t.Fatal(cmd)
		}
		request, e := protocol.UnwrapOIDB(data)
		if e != nil {
			t.Fatal(e)
		}
		f := obFields(request.Body)
		calls++
		if calls == 2 && obNumber(obFields(obBytes(f, 5)), 1) != 123 {
			t.Fatal("next-page cursor missing")
		}
		property := func(k uint64, v string) protocol.Field {
			return protocol.BytesField(2, protocol.Marshal(protocol.VarintField(1, k), protocol.StringField(2, v)))
		}
		friend := protocol.Marshal(protocol.StringField(1, "u_test"), protocol.VarintField(3, uint64(120+calls)), protocol.BytesField(10001, protocol.Marshal(protocol.VarintField(1, 1), protocol.BytesField(2, protocol.Marshal(property(103, "remark"), property(20002, "nickname"))))))
		reply := []protocol.Field{protocol.VarintField(7, 10001), protocol.BytesField(101, friend)}
		if calls == 1 {
			reply = append(reply, protocol.BytesField(2, protocol.Marshal(protocol.VarintField(1, 123))))
		}
		return obFixtureReply(cmd, protocol.Marshal(reply...)), nil
	}))
	out, e := backend.OneBotAction(context.Background(), "get_friend_list", json.RawMessage(`{}`))
	if e != nil {
		t.Fatal(e)
	}
	friends := out.([]map[string]any)
	if len(friends) != 2 || calls != 2 || friends[0]["remark"] != "remark" || backend.uidFor(121) != "u_test" {
		t.Fatal("friend decode or learned UID failed", friends)
	}
	calls = 0
	backend = NewOneBotQueryBackend(obQueryExchange(func(_ context.Context, cmd string, _ []byte) ([]byte, error) {
		calls++
		return obFixtureReply(cmd, protocol.Marshal(protocol.BytesField(2, protocol.Marshal(protocol.VarintField(1, 123))))), nil
	}))
	_, e = backend.OneBotAction(context.Background(), "get_friend_list", json.RawMessage(`{}`))
	if e == nil || calls > 2 {
		t.Fatal("pagination cycle accepted")
	}
}
func TestOneBotGroupMembersAndCrossGroupReply(t *testing.T) {
	backend := NewOneBotQueryBackend(obQueryExchange(func(_ context.Context, cmd string, data []byte) ([]byte, error) {
		if cmd != protocol.OidbCommand(0xfe7, 3) {
			t.Fatal(cmd)
		}
		req, _ := protocol.UnwrapOIDB(data)
		group := obNumber(obFields(req.Body), 1)
		member := protocol.Marshal(protocol.BytesField(1, protocol.Marshal(protocol.StringField(2, "u_member"), protocol.VarintField(4, 123))), protocol.StringField(10, "member"), protocol.BytesField(11, protocol.Marshal(protocol.StringField(2, "card"))), protocol.VarintField(100, 100), protocol.VarintField(101, 200), protocol.VarintField(102, 3210), protocol.VarintField(107, 2))
		return obFixtureReply(cmd, protocol.Marshal(protocol.VarintField(1, uint64(group)), protocol.BytesField(2, member))), nil
	}))
	out, e := backend.OneBotAction(context.Background(), "get_group_member_info", json.RawMessage(`{"group_id":90001,"user_id":123}`))
	if e != nil {
		t.Fatal(e)
	}
	m := out.(map[string]any)
	if m["role"] != "admin" || m["card"] != "card" || backend.uidFor(123) != "u_member" {
		t.Fatal(m)
	}
	if m["shut_up_timestamp"] != uint32(3210) || m["title_expire_time"] != 0 {
		t.Fatal("member compatibility fields", m)
	}
	backend.exchange = obQueryExchange(func(_ context.Context, cmd string, _ []byte) ([]byte, error) {
		return obFixtureReply(cmd, protocol.Marshal(protocol.VarintField(1, 99999))), nil
	})
	if _, e = backend.OneBotAction(context.Background(), "get_group_member_list", json.RawMessage(`{"group_id":90001}`)); e == nil {
		t.Fatal("wrong group response accepted")
	}
}

func TestStrangerInfoReturnsPersonalRemarkAndSignature(t *testing.T) {
	backend := NewOneBotQueryBackend(obQueryExchange(func(_ context.Context, cmd string, data []byte) ([]byte, error) {
		envelope, _ := protocol.UnwrapOIDB(data)
		fields := obFields(envelope.Body)
		requested := map[uint32]bool{}
		for _, f := range fields {
			if f.Tag == 3 {
				requested[obNumber(obFields(f.Data), 1)] = true
			}
		}
		if !requested[103] || !requested[102] {
			t.Fatal("profile request omitted remark/signature")
		}
		property := func(k uint64, text string) protocol.Field {
			return protocol.BytesField(2, protocol.Marshal(protocol.VarintField(1, k), protocol.StringField(2, text)))
		}
		properties := protocol.Marshal(property(20002, "May"), property(103, "friend label"), property(102, "A personal signature"))
		return obFixtureReply(cmd, protocol.Marshal(protocol.BytesField(1, protocol.Marshal(protocol.VarintField(3, 123), protocol.BytesField(2, properties))))), nil
	}))
	out, err := backend.OneBotAction(context.Background(), "get_stranger_info", json.RawMessage(`{"user_id":123}`))
	if err != nil {
		t.Fatal(err)
	}
	data := out.(map[string]any)
	if data["remark"] != "friend label" || data["long_nick"] != "A personal signature" || data["nickname"] != "May" {
		t.Fatal(data)
	}
}
func TestOneBotQueriesRejectInvalidIDsAndServerErrors(t *testing.T) {
	backend := NewOneBotQueryBackend(obQueryExchange(func(context.Context, string, []byte) ([]byte, error) { return nil, fmt.Errorf("server refused") }))
	for _, p := range []string{`{"group_id":0}`, `{"group_id":4294967296}`} {
		if _, e := backend.OneBotAction(context.Background(), "get_group_member_list", json.RawMessage(p)); e == nil {
			t.Fatal("bad ID accepted")
		}
	}
	if _, e := backend.OneBotAction(context.Background(), "get_friend_list", json.RawMessage(`{}`)); e == nil {
		t.Fatal("error hidden as empty friends")
	}
}
