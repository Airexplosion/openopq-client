package client

import (
	"context"
	"testing"

	"openopq/internal/protocol"
)

func sendRouting(t *testing.T, b *Bot, fake *fakeExchange, target protocol.SendTarget) map[int]any {
	t.Helper()
	fake.replies = append(fake.replies, protocol.Marshal(protocol.VarintField(14, 123)))
	if _, err := b.SendMessage(context.Background(), target, "synthetic test", nil); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if len(fake.calls) == 0 || fake.calls[len(fake.calls)-1] != protocol.SendMessageCommand {
		t.Fatalf("send command = %v", fake.calls)
	}
	outer, err := protocol.Unmarshal(fake.sent[len(fake.sent)-1])
	if err != nil {
		t.Fatalf("outgoing message: %v", err)
	}
	raw, ok := outer[1].([]byte)
	if !ok {
		t.Fatalf("missing outgoing routing: %#v", outer)
	}
	routing, err := protocol.Unmarshal(raw)
	if err != nil {
		t.Fatalf("outgoing routing: %v", err)
	}
	return routing
}

func privateRouting(t *testing.T, routing map[int]any) map[int]any {
	t.Helper()
	raw, ok := routing[1].([]byte)
	if !ok {
		t.Fatalf("missing C2C routing: %#v", routing)
	}
	peer, err := protocol.Unmarshal(raw)
	if err != nil {
		t.Fatalf("C2C routing: %v", err)
	}
	return peer
}

func TestSendMessagePrivateUIDUsesOnlyThisBotsContacts(t *testing.T) {
	const peerUin = uint32(10001)
	first, firstWire := connectedBot(t)
	second, secondWire := connectedBot(t)
	first.remember(peerUin, "u_first_view", "")
	second.remember(peerUin, "u_second_view", "")
	for _, test := range []struct {
		bot     *Bot
		wire    *fakeExchange
		wantUID string
	}{
		{first, firstWire, "u_first_view"},
		{second, secondWire, "u_second_view"},
		{first, firstWire, "u_first_view"},
	} {
		peer := privateRouting(t, sendRouting(t, test.bot, test.wire, protocol.SendTarget{Uin: peerUin}))
		uid, ok := peer[2].([]byte)
		if peer[1] != uint64(peerUin) || !ok || string(uid) != test.wantUID {
			t.Fatalf("private route = %#v, want UIN %d and UID %q", peer, peerUin, test.wantUID)
		}
	}
}

func TestSendMessagePrivateExplicitUIDTakesPrecedence(t *testing.T) {
	b, fake := connectedBot(t)
	b.remember(10001, "u_cached", "")
	peer := privateRouting(t, sendRouting(t, b, fake, protocol.SendTarget{Uin: 10001, Uid: "u_explicit"}))
	if uid, ok := peer[2].([]byte); !ok || string(uid) != "u_explicit" {
		t.Fatalf("explicit UID was replaced: %#v", peer)
	}
}

func TestSendMessagePrivateUnknownUIDRemainsUnknown(t *testing.T) {
	b, fake := connectedBot(t)
	b.remember(10002, "u_other_peer", "")
	peer := privateRouting(t, sendRouting(t, b, fake, protocol.SendTarget{Uin: 10001}))
	if _, present := peer[2]; present {
		t.Fatalf("unknown peer acquired a UID: %#v", peer)
	}
}

func TestSendMessageGroupKeepsGroupRoutingDespiteCachedPeer(t *testing.T) {
	b, fake := connectedBot(t)
	b.remember(10001, "u_cached", "")
	routing := sendRouting(t, b, fake, protocol.SendTarget{Group: 20001, Uin: 10001})
	if _, present := routing[1]; present {
		t.Fatalf("group send acquired C2C routing: %#v", routing)
	}
	raw, ok := routing[2].([]byte)
	if !ok {
		t.Fatalf("missing group routing: %#v", routing)
	}
	group, err := protocol.Unmarshal(raw)
	if err != nil || group[1] != uint64(20001) || len(group) != 1 {
		t.Fatalf("group route = %#v, err = %v", group, err)
	}
}
