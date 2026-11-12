package client

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"openopq/internal/protocol"
)

func temporaryMember(uin uint32, uid string) protocol.Field {
	return protocol.BytesField(2, protocol.Marshal(protocol.BytesField(1, protocol.Marshal(protocol.StringField(2, uid), protocol.VarintField(4, uint64(uin))))))
}
func TestTemporarySendResolvesExactCommonGroupAndPropagatesQQRefusal(t *testing.T) {
	queries, sends := 0, 0
	refuse := false
	var sent []byte
	b := NewOneBotQueryBackend(obQueryExchange(func(_ context.Context, cmd string, body []byte) ([]byte, error) {
		switch cmd {
		case protocol.OidbCommand(0xfe7, 3):
			queries++
			return obFixtureReply(cmd, protocol.Marshal(protocol.VarintField(1, 90001), temporaryMember(10001, "u_self"), temporaryMember(123, "u_peer"))), nil
		case protocol.SendMessageCommand:
			sends++
			sent = body
			if refuse {
				return protocol.Marshal(protocol.VarintField(1, 102), protocol.StringField(2, "temporary session denied")), nil
			}
			return protocol.Marshal(protocol.VarintField(14, 99), protocol.VarintField(3, 456)), nil
		default:
			return nil, fmt.Errorf("unexpected command %s", cmd)
		}
	}))
	b.account.Uin = 10001
	for i := 0; i < 2; i++ {
		result, err := b.SendMessage(context.Background(), protocol.SendTarget{Uin: 123, TempGroup: 90001}, "hello", nil)
		if err != nil || result.Sequence != 99 || result.Random == 0 || result.ClientSequence == 0 {
			t.Fatal(result, err)
		}
	}
	if queries != 1 || sends != 2 {
		t.Fatal("temporary context not reused", queries, sends)
	}
	fields, _ := protocol.EveryField(sent)
	route, _ := protocol.EveryField(obBytes(fields, 1))
	temp := obFields(obBytes(route, 3))
	if obNumber(temp, 3) != 90001 || obString(temp, 4) != "u_peer" || len(route) != 1 {
		t.Fatal("temporary send used an ordinary/group route", route)
	}
	refuse = true
	_, err := b.SendMessage(context.Background(), protocol.SendTarget{Uin: 123, TempGroup: 90001}, "hello", nil)
	if err == nil || !strings.Contains(err.Error(), "102") || sends != 3 {
		t.Fatal("QQ refusal was hidden or retried through C2C", err, sends)
	}
}

func TestTemporarySendRejectsWrongGroupAndUIDBeforeUploading(t *testing.T) {
	queries := 0
	b := NewOneBotQueryBackend(obQueryExchange(func(_ context.Context, cmd string, _ []byte) ([]byte, error) {
		if cmd != protocol.OidbCommand(0xfe7, 3) {
			t.Fatal("invalid target caused upload/send", cmd)
		}
		queries++
		return obFixtureReply(cmd, protocol.Marshal(protocol.VarintField(1, 90001), temporaryMember(123, "u_peer"))), nil
	}))
	b.account.Uin = 10001
	b.remember(123, "u_peer", "") // A known UID alone must not bypass source-group membership.
	_, err := b.SendRichMessage(context.Background(), protocol.SendTarget{Uin: 123, TempGroup: 90001}, []protocol.SendSegment{{Type: "image", Bytes: []byte{1}}})
	if err == nil || queries != 1 {
		t.Fatal("bot was not a member of the source group", err, queries)
	}
	b.tempRoutes.put(10001, 123, 90001, "u_peer", true)
	if _, err = b.PrepareSendTarget(context.Background(), protocol.SendTarget{Uin: 123, Uid: "u_wrong", TempGroup: 90001}); err == nil {
		t.Fatal("caller UID overrode trusted source-group context")
	}
}

func TestTemporaryReplyHintsAreAccountGroupAndTimeScoped(t *testing.T) {
	a, b := NewOneBotQueryBackend(nil), NewOneBotQueryBackend(nil)
	a.account.Uin, b.account.Uin = 10001, 10002
	a.observeTemporaryMessage(&protocol.IncomingMessage{IsTemporary: true, TempGroupUin: 90001, SenderUin: 123, SenderUid: "u_view_a"})
	b.observeTemporaryMessage(&protocol.IncomingMessage{IsTemporary: true, TempGroupUin: 90002, SenderUin: 123, SenderUid: "u_view_b"})
	for _, c := range []struct {
		bot   *Bot
		group uint32
		uid   string
	}{{a, 90001, "u_view_a"}, {b, 90002, "u_view_b"}} {
		target, err := c.bot.PrepareSendTarget(context.Background(), protocol.SendTarget{Uin: 123})
		if err != nil || target.TempGroup != c.group || target.Uid != c.uid || target.IsGroup() {
			t.Fatal(target, err)
		}
	}
	a.observeTemporaryMessage(&protocol.IncomingMessage{IsTemporary: true, TempGroupUin: 90003, SenderUin: 123, SenderUid: "u_view_a"})
	if _, err := a.PrepareSendTarget(context.Background(), protocol.SendTarget{Uin: 123}); err == nil {
		t.Fatal("multiple source groups silently chose a route")
	}
	if target, err := a.PrepareSendTarget(context.Background(), protocol.SendTarget{Uin: 123, TempGroup: 90003}); err != nil || target.TempGroup != 90003 {
		t.Fatal(target, err)
	}
	a.observeTemporaryMessage(&protocol.IncomingMessage{SenderUin: 123})
	if target, err := a.PrepareSendTarget(context.Background(), protocol.SendTarget{Uin: 123}); err != nil || target.IsTemporary() {
		t.Fatal("ordinary conversation inherited a temporary route", target, err)
	}
	b.tempRoutes.mu.Lock()
	for key, entry := range b.tempRoutes.entries {
		entry.Until = time.Now().Add(-time.Second)
		b.tempRoutes.entries[key] = entry
	}
	b.tempRoutes.mu.Unlock()
	if target, err := b.PrepareSendTarget(context.Background(), protocol.SendTarget{Uin: 123}); err != nil || target.IsTemporary() {
		t.Fatal("expired temporary context reused", target, err)
	}
	a.observeTemporaryMessage(&protocol.IncomingMessage{IsTemporary: true, SenderUin: 999, SenderUid: "u_unknown_source"})
	if _, err := a.PrepareSendTarget(context.Background(), protocol.SendTarget{Uin: 999}); err == nil {
		t.Fatal("missing source group was sent as ordinary C2C")
	}
}

func TestTemporaryHintsDoNotSurviveAnAccountChange(t *testing.T) {
	b := NewOneBotQueryBackend(nil)
	b.account.Uin = 10001
	b.observeTemporaryMessage(&protocol.IncomingMessage{IsTemporary: true, TempGroupUin: 90001, SenderUin: 123, SenderUid: "u_peer"})
	b.account.Uin = 10002
	target, err := b.PrepareSendTarget(context.Background(), protocol.SendTarget{Uin: 123})
	if err != nil || target.IsTemporary() {
		t.Fatal("prior account's reply context survived", target, err)
	}
}
