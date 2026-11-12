package client

import (
	"context"
	"fmt"

	"openopq/internal/protocol"
)

// These are the group/friend management actions the OPQ LuaApiCaller and the OneBot action
// endpoints map onto. A member is named by uin; the NT requests need the uid, which is resolved
// from what received messages have taught — so an action on someone the bot has never seen
// cannot be addressed, and says so.

// uidOrErr resolves a uin to the uid an action needs, or reports that it is unknown.
func (b *Bot) uidOrErr(uin uint32) (string, error) {
	if uid := b.uidFor(uin); uid != "" {
		return uid, nil
	}
	return "", fmt.Errorf("client: no uid known for uin %d (the bot has not seen them in this session)", uin)
}

// oidbDo sends an Oidb action and checks its reply.
func (b *Bot) oidbDo(ctx context.Context, body []byte, command string) error {
	exchange, err := b.service()
	if err != nil {
		return err
	}
	answer, err := exchange.Exchange(ctx, command, body)
	if err != nil {
		return err
	}
	return protocol.ParseOidbReply(answer)
}

// RecallGroupMessage recalls a group message by its sequence.
func (b *Bot) RecallGroupMessage(ctx context.Context, groupUin, seq uint32) error {
	exchange, err := b.service()
	if err != nil {
		return err
	}
	_, err = exchange.Exchange(ctx, protocol.RecallGroupCommand, protocol.BuildRecallGroup(groupUin, seq))
	return err
}

// MuteMember mutes a member for the given number of seconds, or lifts the mute with 0.
func (b *Bot) MuteMember(ctx context.Context, groupUin, uin, duration uint32) error {
	uid, err := b.uidOrErr(uin)
	if err != nil {
		return err
	}
	body, cmd := protocol.BuildMuteMember(groupUin, uid, duration)
	return b.oidbDo(ctx, body, cmd)
}

// MuteGroup mutes or unmutes a whole group.
func (b *Bot) MuteGroup(ctx context.Context, groupUin uint32, mute bool) error {
	body, cmd := protocol.BuildMuteGroup(groupUin, mute)
	return b.oidbDo(ctx, body, cmd)
}

// KickMember removes a member, optionally refusing any later request to rejoin.
func (b *Bot) KickMember(ctx context.Context, groupUin, uin uint32, rejectAddRequest bool) error {
	uid, err := b.uidOrErr(uin)
	if err != nil {
		return err
	}
	body, cmd := protocol.BuildKickMember(groupUin, uid, rejectAddRequest)
	return b.oidbDo(ctx, body, cmd)
}

// SetAdmin grants or revokes a member's admin.
func (b *Bot) SetAdmin(ctx context.Context, groupUin, uin uint32, admin bool) error {
	uid, err := b.uidOrErr(uin)
	if err != nil {
		return err
	}
	body, cmd := protocol.BuildSetAdmin(groupUin, uid, admin)
	return b.oidbDo(ctx, body, cmd)
}

// SetMemberCard sets a member's group card (an empty name clears it).
func (b *Bot) SetMemberCard(ctx context.Context, groupUin, uin uint32, name string) error {
	uid, err := b.uidOrErr(uin)
	if err != nil {
		return err
	}
	body, cmd := protocol.BuildSetMemberCard(groupUin, uid, name)
	return b.oidbDo(ctx, body, cmd)
}

// SetGroupName changes a group's name.
func (b *Bot) SetGroupName(ctx context.Context, groupUin uint32, name string) error {
	body, cmd := protocol.BuildSetGroupName(groupUin, name)
	return b.oidbDo(ctx, body, cmd)
}

// LeaveGroup quits a group.
func (b *Bot) LeaveGroup(ctx context.Context, groupUin uint32) error {
	body, cmd := protocol.BuildLeaveGroup(groupUin)
	return b.oidbDo(ctx, body, cmd)
}

// SetSpecialTitle sets a member's special title.
func (b *Bot) SetSpecialTitle(ctx context.Context, groupUin, uin uint32, title string) error {
	uid, err := b.uidOrErr(uin)
	if err != nil {
		return err
	}
	body, cmd := protocol.BuildSpecialTitle(groupUin, uid, title)
	return b.oidbDo(ctx, body, cmd)
}

// PokeMember pokes a member in a group (the target is named by uin, which the poke carries
// directly, so no uid lookup is needed).
func (b *Bot) PokeMember(ctx context.Context, groupUin, uin uint32) error {
	body, cmd := protocol.BuildPokeGroup(groupUin, uin)
	return b.oidbDo(ctx, body, cmd)
}

// PokeFriend pokes a friend.
func (b *Bot) PokeFriend(ctx context.Context, uin uint32) error {
	body, cmd := protocol.BuildPokeFriend(uin)
	return b.oidbDo(ctx, body, cmd)
}
