package client

import (
	"context"
	"errors"
	"testing"
	"time"

	"openopq/internal/protocol"
)

func userInfoFixture(cmd string, uin uint32, nickname string) []byte {
	return obFixtureReply(cmd, protocol.Marshal(protocol.BytesField(1, protocol.Marshal(protocol.VarintField(3, uint64(uin)), protocol.BytesField(2, protocol.Marshal(protocol.BytesField(2, protocol.Marshal(protocol.VarintField(1, 20002), protocol.StringField(2, nickname)))))))))
}

func TestPrivateNicknameLookupAndGroupCardIsolation(t *testing.T) {
	calls := 0
	b := NewOneBotQueryBackend(obQueryExchange(func(ctx context.Context, cmd string, _ []byte) ([]byte, error) {
		calls++
		if cmd != protocol.OidbCommand(0xfe1, 2) {
			t.Fatal(cmd)
		}
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 2*time.Second {
			t.Fatal("profile lookup unbounded")
		}
		return userInfoFixture(cmd, 456, "真实昵称"), nil
	}))
	message := &protocol.IncomingMessage{SenderUin: 456, SenderUid: "u_peer", Content: ".r 1d6"}
	b.enrichSender(context.Background(), message)
	if message.SenderNick != "真实昵称" || message.Content != ".r 1d6" || calls != 1 {
		t.Fatal("missing nickname or changed content", message)
	}
	group := &protocol.IncomingMessage{IsGroup: true, GroupUin: 90001, SenderUin: 456, GroupCard: ".sn 新名字"}
	b.enrichSender(context.Background(), group)
	private := &protocol.IncomingMessage{SenderUin: 456}
	b.enrichSender(context.Background(), private)
	if group.GroupCard != ".sn 新名字" || private.SenderNick != "真实昵称" || calls != 1 {
		t.Fatal("group card polluted personal nickname")
	}
	other := NewOneBotQueryBackend(obQueryExchange(func(context.Context, string, []byte) ([]byte, error) { return nil, errors.New("offline") }))
	if other.nickFor(456) != "" {
		t.Fatal("nickname cache crossed bot accounts")
	}
}

func TestUnknownNicknameDoesNotDropMessagesOrQueryEveryTime(t *testing.T) {
	calls := 0
	b := NewOneBotQueryBackend(obQueryExchange(func(context.Context, string, []byte) ([]byte, error) {
		calls++
		return nil, errors.New("user info unavailable")
	}))
	for i := 0; i < 3; i++ {
		m := &protocol.IncomingMessage{SenderUin: 456, Content: "keep this"}
		b.enrichSender(context.Background(), m)
		if m.Content != "keep this" || m.SenderNick != "" {
			t.Fatal("invented name or damaged message")
		}
	}
	if calls != 1 {
		t.Fatal("failed user-info lookup repeated for each message")
	}
}
