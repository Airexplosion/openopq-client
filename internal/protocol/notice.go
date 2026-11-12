package protocol

import (
	"encoding/binary"
	"strconv"
	"strings"
)

// Notice is a decoded server push that is not a chat message — a membership change, a recall, a
// mute, an admin change or a poke. Like IncomingMessage it is decoded once, from the same
// olpush MsgPush frame, and each protocol adapter formats its own wire shape from it.
//
// The push identifies people by their modern uid; the uin fields are filled by the client layer
// from what it has learned, and stay zero for anyone it has not seen. A poke is the exception —
// its template carries uins directly.
type NoticeKind string

const (
	NoticeFriendRequest NoticeKind = "friend_request"
	NoticeGroupRequest  NoticeKind = "group_request"
	NoticeGroupJoin     NoticeKind = "group_join"    // a member joined (type 33)
	NoticeGroupLeave    NoticeKind = "group_leave"   // a member left or was kicked (type 34)
	NoticeGroupAdmin    NoticeKind = "group_admin"   // an admin was set or unset (type 44)
	NoticeGroupRecall   NoticeKind = "group_recall"  // a group message was recalled (732/17)
	NoticeGroupMute     NoticeKind = "group_mute"    // a member or the group was muted/unmuted (732/12)
	NoticeGroupPoke     NoticeKind = "group_poke"    // a poke in a group (732/20, busiType 12)
	NoticeFriendRecall  NoticeKind = "friend_recall" // a friend message was recalled (528/138)
	NoticeFriendPoke    NoticeKind = "friend_poke"   // a poke in a private chat (528/290, busiType 12)
)

// Notice carries the fields the adapters need; which are set depends on Kind.
type Notice struct {
	Kind                   NoticeKind
	Comment, SubType, Flag string
	RequestEvent           uint32
	GroupUin               uint32
	UserUin                uint32 // the subject: joiner/leaver, recalled author, muted member, admin target, poke sender
	UserUid                string
	OperatorUin            uint32 // who acted: inviter/kicker/muter/recaller
	OperatorUid            string
	// recall
	MsgSeq    uint32
	MsgRandom uint32
	MsgTime   uint32
	// membership
	ExitType     uint32 // decrease type: 3=self kicked, 131=member kicked, else leave
	IncreaseType uint32 // group-change operation code at field 4, not invitation source at field 6
	// mute
	Duration uint32 // seconds; 0 = lifted, 0xFFFFFFFF = whole group
	// admin
	Enable bool // admin granted (true) or revoked (false)
	// poke
	Target         uint32 // poke receiver uin
	Suffix         string
	Action         string // e.g. "戳了戳"
	ActionImageURL string
}

// unwrap732 peels the shell a 0x2DC (732) push body carries: a 4-byte group uin, a reserved
// byte, then the inner NotifyMessageBody behind a 2-byte length.
func unwrap732(content []byte) (groupUin uint32, inner []byte, ok bool) {
	if len(content) < 7 {
		return 0, nil, false
	}
	groupUin = binary.BigEndian.Uint32(content[0:4])
	n := int(binary.BigEndian.Uint16(content[5:7]))
	if 7+n > len(content) {
		return 0, nil, false
	}
	return groupUin, content[7 : 7+n], true
}

// isNoticeType reports whether a ContentHead.Type routes to a notice rather than a chat message.
func isNoticeType(t uint32) bool {
	switch t {
	case 33, 34, 44, 84, 87, 525, 0x210, 0x2DC:
		return true
	}
	return false
}

// decodeNotice decodes a push body (Body.MsgContent) for a notice ContentHead.Type/SubType.
// It returns nil for a shape it does not handle, so an unknown sub-type is simply ignored.
func decodeNotice(chType, chSub uint32, content []byte) *Notice {
	switch chType {
	case 84:
		f, e := EveryField(content)
		if e != nil {
			return nil
		}
		return &Notice{Kind: NoticeGroupRequest, GroupUin: pbU32(f, 1), UserUid: pbStr(f, 3), Comment: pbStr(f, 5), SubType: "add", RequestEvent: 1}
	case 87:
		f, e := EveryField(content)
		if e != nil {
			return nil
		}
		return &Notice{Kind: NoticeGroupRequest, GroupUin: pbU32(f, 1), UserUid: pbStr(f, 5), SubType: "invite", RequestEvent: 2}
	case 525:
		f, e := EveryField(content)
		if e != nil || pbU32(f, 1) != 87 {
			return nil
		}
		a, _ := EveryField(pbBytes(f, 2))
		v, _ := EveryField(pbBytes(a, 1))
		return &Notice{Kind: NoticeGroupRequest, GroupUin: pbU32(v, 1), UserUid: pbStr(v, 5), OperatorUid: pbStr(v, 6), SubType: "add", RequestEvent: 22}
	case 33: // member increase
		f, err := EveryField(content)
		if err != nil {
			return nil
		}
		return &Notice{
			Kind: NoticeGroupJoin, GroupUin: pbU32(f, 1),
			UserUid: pbStr(f, 3), OperatorUid: noticeOperatorUID(pbBytes(f, 5)), IncreaseType: pbU32(f, 4),
		}

	case 34: // member decrease
		f, err := EveryField(content)
		if err != nil {
			return nil
		}
		n := &Notice{Kind: NoticeGroupLeave, GroupUin: pbU32(f, 1), UserUid: pbStr(f, 3), ExitType: pbU32(f, 4)}
		n.OperatorUid = noticeOperatorUID(pbBytes(f, 5))
		if n.ExitType == 130 {
			n.OperatorUid = n.UserUid
		}
		return n

	case 44: // admin change
		f, err := EveryField(content)
		if err != nil {
			return nil
		}
		n := &Notice{Kind: NoticeGroupAdmin, GroupUin: pbU32(f, 1)}
		if body, ok := FirstField(f, 4); ok && body.IsData {
			bf, _ := EveryField(body.Data)
			if en, ok := FirstField(bf, 2); ok && en.IsData { // ExtraEnable
				ef, _ := EveryField(en.Data)
				n.Enable, n.UserUid = true, pbStr(ef, 1)
			} else if dis, ok := FirstField(bf, 1); ok && dis.IsData { // ExtraDisable
				ef, _ := EveryField(dis.Data)
				n.Enable, n.UserUid = false, pbStr(ef, 1)
			}
		}
		return n

	case 0x210: // friend event
		f, err := EveryField(content)
		if err != nil {
			return nil
		}
		switch chSub {
		case 35:
			inf, _ := EveryField(pbBytes(f, 1))
			return &Notice{Kind: NoticeFriendRequest, UserUid: pbStr(inf, 2), Comment: pbStr(inf, 10)}
		case 138: // friend recall
			if info, ok := FirstField(f, 1); ok && info.IsData {
				inf, _ := EveryField(info.Data)
				return &Notice{
					Kind: NoticeFriendRecall, UserUid: pbStr(inf, 1),
					MsgSeq: pbU32(inf, 3), MsgTime: pbU32(inf, 5), MsgRandom: pbU32(inf, 6),
				}
			}
		case 290: // friend poke (grey tip)
			if pbU64(f, 1) == 12 { // BusiType
				return parsePoke(f, 0, NoticeFriendPoke)
			}
		}

	case 0x2DC: // group event
		switch chSub {
		case 12: // mute (direct GroupMute, no shell)
			f, err := EveryField(content)
			if err != nil {
				return nil
			}
			n := &Notice{Kind: NoticeGroupMute, GroupUin: pbU32(f, 1), OperatorUid: pbStr(f, 4)}
			if data, ok := FirstField(f, 5); ok && data.IsData {
				df, _ := EveryField(data.Data)
				if st, ok := FirstField(df, 3); ok && st.IsData {
					sf, _ := EveryField(st.Data)
					n.UserUid = pbStr(sf, 1)
					n.Duration = pbU32(sf, 2)
				}
			}
			return n

		case 17: // group recall (shell)
			gu, inner, ok := unwrap732(content)
			if !ok {
				return nil
			}
			nb, err := EveryField(inner)
			if err != nil {
				return nil
			}
			n := &Notice{Kind: NoticeGroupRecall, GroupUin: pbU32(nb, 4)}
			if n.GroupUin == 0 {
				n.GroupUin = gu
			}
			if rc, ok := FirstField(nb, 11); ok && rc.IsData {
				rf, _ := EveryField(rc.Data)
				n.OperatorUid = pbStr(rf, 1)
				if msgs := FieldsWith(rf, 3); len(msgs) > 0 && msgs[0].IsData {
					mf, _ := EveryField(msgs[0].Data)
					n.MsgSeq = pbU32(mf, 1)
					n.MsgTime = pbU32(mf, 2)
					n.MsgRandom = pbU32(mf, 3)
					n.UserUid = pbStr(mf, 6)
				}
			}
			return n

		case 20: // group poke (shell)
			gu, inner, ok := unwrap732(content)
			if !ok {
				return nil
			}
			nb, err := EveryField(inner)
			if err != nil {
				return nil
			}
			if gt, ok := FirstField(nb, 26); ok && gt.IsData { // GrayTipInfo
				gf, _ := EveryField(gt.Data)
				if pbU64(gf, 1) == 12 { // BusiType
					return parsePoke(gf, gu, NoticeGroupPoke)
				}
			}
		}
	}
	return nil
}

// Group-change operator bytes may carry a UID directly or OperatorInfo.
// Never feed serialized protobuf bytes into the account's UID lookup.
func noticeOperatorUID(data []byte) string {
	if strings.HasPrefix(string(data), "u_") {
		return string(data)
	}
	outer, err := EveryField(data)
	if err != nil {
		return ""
	}
	inner, err := EveryField(pbBytes(outer, 1))
	if err != nil {
		return ""
	}
	return pbStr(inner, 1)
}

// parsePoke reads a GeneralGrayTipInfo's template params into a poke notice. The template
// carries the sender and receiver as uins (numeric strings), not uids.
func parsePoke(grayTip []Field, groupUin uint32, kind NoticeKind) *Notice {
	n := &Notice{Kind: kind, GroupUin: groupUin, Action: "戳了戳"}
	for _, p := range FieldsWith(grayTip, 7) { // MsgTemplParam
		if !p.IsData {
			continue
		}
		pf, err := EveryField(p.Data)
		if err != nil {
			continue
		}
		key, val := pbStr(pf, 1), pbStr(pf, 2)
		switch key {
		case "uin_str1":
			n.UserUin = atou32(val)
		case "uin_str2":
			n.Target = atou32(val)
		case "suffix_str":
			n.Suffix = val
		case "action_img_url":
			n.ActionImageURL = val
		case "alt_str1":
			if val != "" {
				n.Action = val
			}
		}
	}
	return n
}

func atou32(s string) uint32 {
	v, _ := strconv.ParseUint(s, 10, 32)
	return uint32(v)
}
