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

func (b *Bot) uidOrFetch(ctx context.Context, uin uint32) (string, error) {
	if uid := b.uidFor(uin); uid != "" {
		return uid, nil
	}
	if _, err := b.oneBotFriends(ctx); err != nil {
		return "", err
	}
	return b.uidOrErr(uin)
}

func (b *Bot) rememberHistory(messages []*protocol.IncomingMessage) {
	for _, m := range messages {
		if m == nil {
			continue
		}
		b.remember(m.SenderUin, m.SenderUid, m.SenderNick)
		if !m.IsGroup && m.ToUin != 0 {
			b.remember(m.ToUin, m.ToUid, "")
		}
	}
}

// SetFriendRemark sets the local remark for a friend. Empty remark clears it.
func (b *Bot) SetFriendRemark(ctx context.Context, uin uint32, remark string) error {
	exchange, err := b.service()
	if err != nil {
		return err
	}
	_, err = exchange.Exchange(ctx, "ProfileService.ChangeFriendName", protocol.BuildFriendRemark(uin, remark))
	return err
}

// DeleteFriend removes a friend. block asks QQ to refuse later add requests.
func (b *Bot) DeleteFriend(ctx context.Context, uin uint32, block bool) error {
	uid, err := b.uidOrFetch(ctx, uin)
	if err != nil {
		return err
	}
	body, cmd := protocol.BuildDeleteFriend(uid, block)
	return b.oidbDo(ctx, body, cmd)
}

// MarkGroupRead reports a group conversation as read through startSeq.
func (b *Bot) MarkGroupRead(ctx context.Context, groupUin, startSeq uint32) error {
	exchange, err := b.service()
	if err != nil {
		return err
	}
	_, err = exchange.Exchange(ctx, protocol.ReadedReportCommand, protocol.BuildReadedReportGroup(groupUin, startSeq))
	return err
}

// MarkPrivateRead reports a private conversation as read.
func (b *Bot) MarkPrivateRead(ctx context.Context, uin, timestamp, startSeq uint32) error {
	uid, err := b.uidOrFetch(ctx, uin)
	if err != nil {
		return err
	}
	exchange, err := b.service()
	if err != nil {
		return err
	}
	_, err = exchange.Exchange(ctx, protocol.ReadedReportCommand, protocol.BuildReadedReportC2C(uid, timestamp, startSeq))
	return err
}

// GroupMessages fetches a closed group sequence window from roaming.
func (b *Bot) GroupMessages(ctx context.Context, groupUin, startSeq, endSeq uint32) ([]*protocol.IncomingMessage, error) {
	exchange, err := b.service()
	if err != nil {
		return nil, err
	}
	answer, err := exchange.Exchange(ctx, protocol.GetGroupMsgCommand, protocol.BuildGetGroupMsg(groupUin, startSeq, endSeq))
	if err != nil {
		return nil, err
	}
	messages, err := protocol.ParseGetGroupMsgResponse(answer)
	if err != nil {
		return nil, err
	}
	b.rememberHistory(messages)
	return messages, nil
}

// SetGroupRemark sets the local remark for a group. Empty remark clears it.
func (b *Bot) SetGroupRemark(ctx context.Context, groupUin uint32, remark string) error {
	body, cmd := protocol.BuildSetGroupRemark(groupUin, remark)
	return b.oidbDo(ctx, body, cmd)
}

// SetGroupReaction adds or removes a reaction on a group message.
func (b *Bot) SetGroupReaction(ctx context.Context, groupUin, seq uint32, code string, add bool) error {
	body, cmd := protocol.BuildSetGroupReaction(groupUin, seq, code, add)
	return b.oidbDo(ctx, body, cmd)
}

// SetEssenceMessage sets or clears a group essence message. random is ContentHead.MsgId
// on a received message, or the client random of a sent one.
func (b *Bot) SetEssenceMessage(ctx context.Context, groupUin, seq, random uint32, set bool) error {
	if seq == 0 || random == 0 {
		return fmt.Errorf("essence requires sequence and random")
	}
	body, cmd := protocol.BuildSetEssence(groupUin, seq, random, set)
	return b.oidbDo(ctx, body, cmd)
}

// AtAllRemain reports how many @all mentions this account still has in a group.
func (b *Bot) AtAllRemain(ctx context.Context, groupUin uint32) (protocol.AtAllRemain, error) {
	self, err := b.Self(ctx)
	if err != nil {
		return protocol.AtAllRemain{}, err
	}
	body, cmd := protocol.BuildAtAllRemain(self.Uin, groupUin)
	answer, err := b.oidbAnswer(ctx, body, cmd)
	if err != nil {
		return protocol.AtAllRemain{}, err
	}
	return protocol.ParseAtAllRemain(answer)
}

// CheckURL reports whether a URL is safe (1), unknown (2) or dangerous (3).
func (b *Bot) CheckURL(ctx context.Context, rawURL string) (int, error) {
	self, err := b.Self(ctx)
	if err != nil {
		return protocol.URLUnknown, err
	}
	body, cmd := protocol.BuildURLCheck(self.Uin, rawURL)
	answer, err := b.oidbAnswer(ctx, body, cmd)
	if err != nil {
		return protocol.URLUnknown, err
	}
	return protocol.ParseURLCheck(answer)
}

// GroupSign clocks in for a group. appVersion is the PT version this client claims.
func (b *Bot) GroupSign(ctx context.Context, groupUin uint32) (protocol.GroupSign, error) {
	self, err := b.Self(ctx)
	if err != nil {
		return protocol.GroupSign{}, err
	}
	version := b.options.Device.PTVersion
	if version == "" {
		version = "7.0.2"
	}
	body, cmd := protocol.BuildGroupSign(self.Uin, groupUin, version)
	answer, err := b.oidbAnswer(ctx, body, cmd)
	if err != nil {
		return protocol.GroupSign{}, err
	}
	return protocol.ParseGroupSign(answer)
}

// DomainCookies asks 0x102a_0 for the cookies of one domain (typically a p_skey).
func (b *Bot) DomainCookies(ctx context.Context, domain string) ([]protocol.Cookie, error) {
	ex, err := b.service()
	if err != nil {
		return nil, err
	}
	answer, err := ex.Exchange(ctx, protocol.FetchCookieCommand, protocol.BuildFetchCookie(domain))
	if err != nil {
		return nil, err
	}
	return protocol.ParseCookies(answer)
}

// ClientKey asks 0x102a_1 for the key a web endpoint is called with.
func (b *Bot) ClientKey(ctx context.Context) (protocol.ClientKey, error) {
	ex, err := b.service()
	if err != nil {
		return protocol.ClientKey{}, err
	}
	answer, err := ex.Exchange(ctx, protocol.FetchClientKeyCommand, protocol.BuildFetchClientKey())
	if err != nil {
		return protocol.ClientKey{}, err
	}
	return protocol.ParseClientKey(answer)
}

func (b *Bot) oidbAnswer(ctx context.Context, body []byte, command string) ([]byte, error) {
	ex, err := b.service()
	if err != nil {
		return nil, err
	}
	return ex.Exchange(ctx, command, body)
}

func (b *Bot) FriendMessages(ctx context.Context, uin, timestamp, count uint32) ([]*protocol.IncomingMessage, error) {
	uid, err := b.uidOrFetch(ctx, uin)
	if err != nil {
		return nil, err
	}
	exchange, err := b.service()
	if err != nil {
		return nil, err
	}
	answer, err := exchange.Exchange(ctx, protocol.GetRoamMsgCommand, protocol.BuildGetRoamMsg(uid, timestamp, count))
	if err != nil {
		return nil, err
	}
	messages, err := protocol.ParseGetRoamMsgResponse(answer)
	if err != nil {
		return nil, err
	}
	b.rememberHistory(messages)
	return messages, nil
}
