package client

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"openopq/internal/protocol"
)

func TestRequestFlagsAccountIsolationAndSingleAttempt(t *testing.T) {
	calls := 0
	b := NewOneBotQueryBackend(obQueryExchange(func(_ context.Context, cmd string, data []byte) ([]byte, error) {
		calls++
		if cmd != protocol.OidbCommand(0xb5d, 44) {
			t.Fatal(cmd)
		}
		r, _ := protocol.UnwrapOIDB(data)
		f := obFields(r.Body)
		if obNumber(f, 1) != 3 || obString(f, 2) != "u_applicant" {
			t.Fatal("wrong friend decision")
		}
		return obFixtureReply(cmd, nil), nil
	}))
	flag := b.obRequests.add(pendingRequest{Kind: protocol.NoticeFriendRequest, UID: "u_applicant", Sequence: 123})
	p := json.RawMessage(fmt.Sprintf(`{"flag":%q}`, flag))
	other := NewOneBotQueryBackend(b.exchange)
	if _, e := other.OneBotAction(context.Background(), "set_friend_add_request", p); e == nil {
		t.Fatal("cross-account flag accepted")
	}
	if _, e := b.OneBotAction(context.Background(), "set_group_add_request", p); e == nil {
		t.Fatal("friend flag used for group")
	}
	for i := 0; i < 2; i++ {
		if _, e := b.OneBotAction(context.Background(), "set_friend_add_request", p); e != nil {
			t.Fatal(e)
		}
	}
	if calls != 1 {
		t.Fatal("replayed approval", calls)
	}
	if _, e := b.OneBotAction(context.Background(), "set_friend_add_request", json.RawMessage(fmt.Sprintf(`{"flag":%q,"approve":false}`, flag))); e == nil {
		t.Fatal("changed decision accepted")
	}
	fresh := b.obRequests.add(pendingRequest{Kind: protocol.NoticeFriendRequest, UID: "u_applicant", Sequence: 124})
	if fresh == flag {
		t.Fatal("new request reused processed flag")
	}
	b.obRequests.entries[fresh].Created = time.Now().Add(-25 * time.Hour)
	if _, e := b.OneBotAction(context.Background(), "set_friend_add_request", json.RawMessage(fmt.Sprintf(`{"flag":%q}`, fresh))); e == nil {
		t.Fatal("expired flag accepted")
	}
}
func TestGroupRequestMatchesExactServerSequenceAndFilter(t *testing.T) {
	const sequence = uint64(1)<<40 | 57
	sent := 0
	b := NewOneBotQueryBackend(obQueryExchange(func(_ context.Context, cmd string, data []byte) ([]byte, error) {
		r, _ := protocol.UnwrapOIDB(data)
		if cmd == protocol.OidbCommand(0x10c0, 1) {
			return obFixtureReply(cmd, nil), nil
		}
		if cmd == protocol.OidbCommand(0x10c0, 2) {
			request := protocol.Marshal(protocol.VarintField(1, sequence), protocol.VarintField(2, 22), protocol.VarintField(3, 1), protocol.BytesField(4, protocol.Marshal(protocol.VarintField(1, 90001))), protocol.BytesField(5, protocol.Marshal(protocol.StringField(1, "u_new"))), protocol.StringField(10, "test join"))
			return obFixtureReply(cmd, protocol.Marshal(protocol.BytesField(1, request))), nil
		}
		if cmd != protocol.OidbCommand(0x10c8, 2) {
			t.Fatal(cmd)
		}
		sent++
		f := obFields(r.Body)
		inner := obFields(obBytes(f, 2))
		s, _ := protocol.FirstField(inner, 1)
		if s.Number != sequence || obNumber(inner, 2) != 22 || obNumber(inner, 3) != 90001 || obNumber(f, 1) != 2 || obString(inner, 4) != "测试拒绝" {
			t.Fatal("wrong request body")
		}
		return obFixtureReply(cmd, nil), nil
	}))
	b.remember(123, "u_new", "")
	n := &protocol.Notice{Kind: protocol.NoticeGroupRequest, GroupUin: 90001, UserUid: "u_new", RequestEvent: 22, SubType: "add"}
	if e := b.prepareRequest(context.Background(), n); e != nil {
		t.Fatal(e)
	}
	if n.Flag == "" || n.UserUin != 123 || n.Comment != "test join" {
		t.Fatal(n)
	}
	_, e := b.OneBotAction(context.Background(), "set_group_add_request", json.RawMessage(fmt.Sprintf(`{"flag":%q,"sub_type":"invite"}`, n.Flag)))
	if e == nil || sent != 0 {
		t.Fatal("mismatched subtype sent")
	}
	_, e = b.OneBotAction(context.Background(), "set_group_add_request", json.RawMessage(fmt.Sprintf(`{"flag":%q,"sub_type":"add","approve":false,"reason":"测试拒绝"}`, n.Flag)))
	if e != nil || sent != 1 {
		t.Fatal(e, sent)
	}
	n.UserUid = "u_other"
	if e = b.prepareRequest(context.Background(), n); e == nil {
		t.Fatal("matched unrelated applicant")
	}
}
func TestUncertainApprovalNotRetried(t *testing.T) {
	calls := 0
	b := NewOneBotQueryBackend(obQueryExchange(func(context.Context, string, []byte) ([]byte, error) { calls++; return nil, context.DeadlineExceeded }))
	flag := b.obRequests.add(pendingRequest{Kind: protocol.NoticeFriendRequest, UID: "u_x"})
	p := json.RawMessage(fmt.Sprintf(`{"flag":%q}`, flag))
	for i := 0; i < 2; i++ {
		if _, e := b.OneBotAction(context.Background(), "set_friend_add_request", p); e == nil {
			t.Fatal("uncertain approval reported success")
		}
	}
	if calls != 1 {
		t.Fatal(calls)
	}
}
