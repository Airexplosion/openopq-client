package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	"openopq/internal/protocol"
)

type obPokeBot struct {
	*obTestBot
	group, member, friend uint32
}

func (b *obPokeBot) MuteMember(context.Context, uint32, uint32, uint32) error { return nil }
func (b *obPokeBot) MuteGroup(context.Context, uint32, bool) error            { return nil }
func (b *obPokeBot) KickMember(context.Context, uint32, uint32, bool) error   { return nil }
func (b *obPokeBot) SetAdmin(context.Context, uint32, uint32, bool) error     { return nil }
func (b *obPokeBot) SetMemberCard(context.Context, uint32, uint32, string) error {
	return nil
}
func (b *obPokeBot) SetGroupName(context.Context, uint32, string) error { return nil }
func (b *obPokeBot) LeaveGroup(context.Context, uint32) error           { return nil }
func (b *obPokeBot) SetSpecialTitle(context.Context, uint32, uint32, string) error {
	return nil
}
func (b *obPokeBot) PokeMember(_ context.Context, group, uin uint32) error {
	b.group, b.member = group, uin
	return nil
}
func (b *obPokeBot) PokeFriend(_ context.Context, uin uint32) error {
	b.friend = uin
	return nil
}

func TestSendPokeUsesGroupWhenGroupIDPresent(t *testing.T) {
	_, base := newOBTest(t)
	b := &obPokeBot{obTestBot: base}
	s := NewServer(b, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{OneBot: true})
	defer s.Close()
	r := obHTTP(s, "POST", "/send_poke", `{"user_id":123,"group_id":90001}`, "application/json")
	if r.Code != 200 || !strings.Contains(r.Body.String(), `"status":"ok"`) {
		t.Fatal(r.Body.String())
	}
	if b.group != 90001 || b.member != 123 || b.friend != 0 {
		t.Fatalf("group poke not used: group=%d member=%d friend=%d", b.group, b.member, b.friend)
	}
	r = obHTTP(s, "POST", "/send_poke", `{"user_id":456}`, "application/json")
	if r.Code != 200 || b.friend != 456 {
		t.Fatal("friend poke", r.Body.String(), b.friend)
	}
}

func TestFriendRemarkDeleteHistoryAndReadRoute(t *testing.T) {
	_, base := newOBTest(t)
	b := &obRichTestBot{obTestBot: base}
	s := NewServer(b, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{OneBot: true})
	defer s.Close()
	for _, c := range []struct{ action, params, want string }{
		{"set_friend_remark", `{"user_id":123,"remark":"lab"}`, "set_friend_remark"},
		{"delete_friend", `{"user_id":123,"block":true}`, "delete_friend"},
		{"mark_group_msg_as_read", `{"group_id":90001,"message_seq":9}`, "mark_group_msg_as_read"},
		{"mark_private_msg_as_read", `{"user_id":123,"message_seq":8}`, "mark_private_msg_as_read"},
		{"get_group_msg_history", `{"group_id":90001,"message_seq":20}`, "get_group_msg_history"},
		{"get_friend_msg_history", `{"user_id":123}`, "get_friend_msg_history"},
	} {
		r := obHTTP(s, "POST", "/"+c.action, c.params, "application/json")
		if r.Code != 200 || !strings.Contains(r.Body.String(), `"status":"ok"`) {
			t.Fatal(c.action, r.Body.String())
		}
		if b.actions[len(b.actions)-1].Action != c.want {
			t.Fatal(c.action, b.actions[len(b.actions)-1].Action)
		}
	}
	send := s.handleOneBotAction(context.Background(), obAction{Action: "send_msg", Params: json.RawMessage(`{"group_id":90001,"message":"hi"}`)})
	id := send.Data.(map[string]any)["message_id"].(int32)
	r := s.handleOneBotAction(context.Background(), obAction{Action: "mark_msg_as_read", Params: json.RawMessage(mustJSON(map[string]any{"message_id": id}))})
	if r.Status != "ok" {
		t.Fatal(r)
	}
	last := b.actions[len(b.actions)-1]
	if last.Action != "mark_group_msg_as_read" {
		t.Fatal(last.Action)
	}
	cacheCompatMessage(s, &protocol.IncomingMessage{
		SenderUin: 456, SenderUid: "u_peer", MsgSeq: 8, MsgTime: 200, Content: "hi",
	})
	before := len(b.actions)
	r = s.handleOneBotAction(context.Background(), obAction{Action: "_mark_all_as_read", Params: json.RawMessage(`{}`)})
	if r.Status != "ok" || r.Data != true {
		t.Fatal(r)
	}
	var markedGroup, markedPrivate bool
	for _, a := range b.actions[before:] {
		switch a.Action {
		case "mark_group_msg_as_read":
			markedGroup = true
		case "mark_private_msg_as_read":
			markedPrivate = true
		}
	}
	if !markedGroup || !markedPrivate {
		t.Fatal(b.actions[before:])
	}
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func TestHistoryFormatsIncomingMessages(t *testing.T) {
	_, base := newOBTest(t)
	b := &obHistoryBot{obTestBot: base, messages: []*protocol.IncomingMessage{{
		IsGroup: true, GroupUin: 90001, SenderUin: 123, SenderNick: "n", MsgSeq: 11, MsgTime: 1700000000, Content: "from-history",
	}}}
	s := NewServer(b, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{OneBot: true})
	defer s.Close()
	r := s.handleOneBotAction(context.Background(), obAction{Action: "get_group_msg_history", Params: json.RawMessage(`{"group_id":90001,"message_seq":11}`)})
	if r.Status != "ok" {
		t.Fatal(r)
	}
	data := r.Data.(map[string]any)
	msgs := data["messages"].([]map[string]any)
	gid, _ := msgs[0]["group_id"].(float64)
	if len(msgs) != 1 || msgs[0]["raw_message"] != "from-history" || gid != 90001 {
		t.Fatal(msgs)
	}
}

type obHistoryBot struct {
	*obTestBot
	messages []*protocol.IncomingMessage
}

func TestEssenceAndForwardRoute(t *testing.T) {
	_, base := newOBTest(t)
	b := &obEssenceForwardBot{obRichTestBot: &obRichTestBot{obTestBot: base}}
	s := NewServer(b, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{OneBot: true})
	defer s.Close()
	id := cacheCompatMessage(s, &protocol.IncomingMessage{
		IsGroup: true, GroupUin: 90001, SenderUin: 123, SenderNick: "n",
		MsgSeq: 12, MsgTime: 100, MsgRandom: 99, Content: "pin me",
	})
	r := s.handleOneBotAction(context.Background(), obAction{Action: "set_essence_msg", Params: json.RawMessage(mustJSON(map[string]any{"message_id": id}))})
	if r.Status != "ok" || b.essenceSeq != 12 || b.essenceRandom != 99 || !b.essenceSet {
		t.Fatal(r.Message, b)
	}
	r = s.handleOneBotAction(context.Background(), obAction{Action: "send_forward_msg", Params: json.RawMessage(`{"group_id":90001,"messages":[{"type":"node","data":{"user_id":123,"nickname":"n","content":"hello"}}]}`)})
	if r.Status != "ok" || len(b.forward) != 1 || b.forward[0].SenderUin != 123 {
		t.Fatal(r.Message, b.forward)
	}
	r = s.handleOneBotAction(context.Background(), obAction{Action: "send_forward_msg", Params: json.RawMessage(mustJSON(map[string]any{
		"group_id": 90001,
		"messages": []any{map[string]any{"type": "node", "data": map[string]any{"id": id}}},
	}))})
	if r.Status != "ok" || len(b.forward) != 2 {
		t.Fatal(r.Message, len(b.forward))
	}
	r = s.handleOneBotAction(context.Background(), obAction{Action: "set_msg_emoji_like", Params: json.RawMessage(mustJSON(map[string]any{"message_id": id, "emoji_id": "76"}))})
	if r.Status != "ok" || b.reactSeq != 12 || b.reactCode != "76" || !b.reactAdd {
		t.Fatal(r.Message, b.reactSeq, b.reactCode, b.reactAdd)
	}
}

type obEssenceForwardBot struct {
	*obRichTestBot
	essenceSeq, essenceRandom uint32
	essenceSet                bool
	reactSeq                  uint32
	reactCode                 string
	reactAdd                  bool
	forward                   []protocol.ForwardNode
}

func (b *obEssenceForwardBot) SetEssenceMessage(_ context.Context, group, seq, random uint32, set bool) error {
	if group != 90001 {
		return fmt.Errorf("group")
	}
	b.essenceSeq, b.essenceRandom, b.essenceSet = seq, random, set
	return nil
}

func (b *obEssenceForwardBot) SetGroupReaction(_ context.Context, group, seq uint32, code string, add bool) error {
	if group != 90001 {
		return fmt.Errorf("group")
	}
	b.reactSeq, b.reactCode, b.reactAdd = seq, code, add
	return nil
}

func (b *obEssenceForwardBot) SendForwardMessage(_ context.Context, _ protocol.SendTarget, nodes []protocol.ForwardNode) (protocol.SendResult, error) {
	b.forward = append(b.forward, nodes...)
	return protocol.SendResult{Sequence: 1, Timestamp: 1, Random: 1}, nil
}

func TestForwardSingleAndKickMembers(t *testing.T) {
	_, base := newOBTest(t)
	b := &obKickForwardBot{obEssenceForwardBot: &obEssenceForwardBot{obRichTestBot: &obRichTestBot{obTestBot: base}}}
	s := NewServer(b, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{OneBot: true})
	defer s.Close()
	id := cacheCompatMessage(s, &protocol.IncomingMessage{
		IsGroup: true, GroupUin: 90001, SenderUin: 123, SenderNick: "n", Content: "hi",
	})
	r := s.handleOneBotAction(context.Background(), obAction{Action: "forward_group_single_msg", Params: json.RawMessage(mustJSON(map[string]any{"group_id": 90001, "message_id": id}))})
	if r.Status != "ok" || len(b.forward) != 1 || b.forward[0].SenderUin != 123 {
		t.Fatal(r.Message, b.forward)
	}
	r = s.handleOneBotAction(context.Background(), obAction{Action: "set_group_kick_members", Params: json.RawMessage(`{"group_id":90001,"user_ids":[123,456]}`)})
	if r.Status != "ok" || len(b.kicked) != 2 || b.kicked[0] != 123 || b.kicked[1] != 456 {
		t.Fatal(r.Message, b.kicked)
	}
}

type obKickForwardBot struct {
	*obEssenceForwardBot
	kicked []uint32
}

func (b *obKickForwardBot) MuteMember(context.Context, uint32, uint32, uint32) error { return nil }
func (b *obKickForwardBot) MuteGroup(context.Context, uint32, bool) error            { return nil }
func (b *obKickForwardBot) KickMember(_ context.Context, _, uin uint32, _ bool) error {
	b.kicked = append(b.kicked, uin)
	return nil
}
func (b *obKickForwardBot) SetAdmin(context.Context, uint32, uint32, bool) error { return nil }
func (b *obKickForwardBot) SetMemberCard(context.Context, uint32, uint32, string) error {
	return nil
}
func (b *obKickForwardBot) SetGroupName(context.Context, uint32, string) error { return nil }
func (b *obKickForwardBot) LeaveGroup(context.Context, uint32) error           { return nil }
func (b *obKickForwardBot) SetSpecialTitle(context.Context, uint32, uint32, string) error {
	return nil
}
func (b *obKickForwardBot) PokeMember(context.Context, uint32, uint32) error { return nil }
func (b *obKickForwardBot) PokeFriend(context.Context, uint32) error         { return nil }

func (b *obHistoryBot) OneBotAction(_ context.Context, action string, _ json.RawMessage) (any, error) {
	if action == "get_group_msg_history" || action == "get_friend_msg_history" {
		return b.messages, nil
	}
	return nil, nil
}
