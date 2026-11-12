package client

import (
	"context"
	"testing"

	"openopq/internal/protocol"
)

func TestRichSendPreservesOrderAndRecallMetadata(t *testing.T) {
	calls := 0
	b := NewOneBotQueryBackend(obQueryExchange(func(_ context.Context, cmd string, body []byte) ([]byte, error) {
		calls++
		if cmd != protocol.SendMessageCommand {
			t.Fatal(cmd)
		}
		f := obFields(body)
		route := obFields(obBytes(obFields(obBytes(f, 1)), 1))
		if obString(route, 2) != "u_peer" {
			t.Fatal("private UID missing")
		}
		rt := obFields(obBytes(obFields(obBytes(f, 3)), 1))
		elements := protocol.FieldsWith(rt, 2)
		if len(elements) != 4 {
			t.Fatal(len(elements))
		}
		if obString(obFields(obBytes(obFields(elements[0].Data), 1)), 1) != "before" || obString(obFields(obBytes(obFields(elements[3].Data), 1)), 1) != "after" {
			t.Fatal("text moved")
		}
		if obNumber(obFields(obBytes(obFields(elements[1].Data), 2)), 1) != 14 {
			t.Fatal("face moved")
		}
		extra := obFields(obBytes(obFields(obBytes(obFields(elements[2].Data), 1)), 12))
		if obString(extra, 9) != "u_peer" {
			t.Fatal("mention UID missing")
		}
		return protocol.Marshal(protocol.VarintField(1, 0), protocol.VarintField(3, 123), protocol.VarintField(4, 7)), nil
	}))
	b.remember(123, "u_peer", "peer")
	result, e := b.SendRichMessage(context.Background(), protocol.SendTarget{Uin: 123}, []protocol.SendSegment{{Type: "text", Text: "before"}, {Type: "face", Face: 14}, {Type: "at", At: protocol.SendAt{Uin: 123}}, {Type: "text", Text: "after"}})
	if e != nil || calls != 1 || result.ClientSequence == 0 {
		t.Fatal(result, e, calls)
	}
	if _, e = b.SendRichMessage(context.Background(), protocol.SendTarget{Uin: 123}, []protocol.SendSegment{{Type: "image"}}); e == nil || calls != 1 {
		t.Fatal("invalid upload sent")
	}
}
