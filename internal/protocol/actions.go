package protocol

import "fmt"

// This file builds the group/friend management requests (recall, mute, kick, admin, card, group
// name, leave, poke, special title) and parses their replies. The protobuf shapes and the Oidb
// command/service numbers are read off LagrangeGo's operation.go + packets/oidb.
//
// Most actions are Oidb requests: a small body wrapped in OidbSvcTrpcTcpBase and sent to the
// command "OidbSvcTrpcTcp.0x<hex>_<service>". The two recalls are plain msg_svc requests.

// OidbCommand formats the SSO command string for an Oidb request. The hex is lower-case and at
// least two digits (0x89a_0, 0x1253_1, …), matching the server's routing.
func OidbCommand(command, service uint32) string {
	return fmt.Sprintf("OidbSvcTrpcTcp.0x%02x_%d", command, service)
}

// buildOidb wraps an inner body in OidbSvcTrpcTcpBase{ Command(1), SubCommand(2), Body(4),
// Reserved(12)=0 }. LagrangeGo sends these actions with Reserved=0 even though the body names
// people by uid.
func buildOidb(command, service uint32, body []byte) []byte {
	return buildOidbReserved(command, service, body, 0)
}

func buildOidbReserved(command, service uint32, body []byte, reserved uint64) []byte {
	return Marshal(
		VarintField(1, uint64(command)),
		VarintField(2, uint64(service)),
		BytesField(4, body),
		VarintField(12, reserved),
	)
}

// ParseOidbReply reads an Oidb reply and turns a non-zero ErrorCode into an error. The reply
// opens with the usual four-byte SSO body length.
func ParseOidbReply(answer []byte) error {
	answer = stripSSOBodyLength(answer)
	fields, err := EveryField(answer)
	if err != nil {
		return fmt.Errorf("protocol: oidb reply: %w", err)
	}
	if code := pbU32(fields, 3); code != 0 { // ErrorCode
		return fmt.Errorf("protocol: action refused: code=%d %s", code, pbStr(fields, 5))
	}
	return nil
}

// --- recall (plain msg_svc, not Oidb) ---

const (
	RecallGroupCommand = "trpc.msg.msg_svc.MsgService.SsoGroupRecallMsg"
	RecallC2CCommand   = "trpc.msg.msg_svc.MsgService.SsoC2CRecallMsg"
)

// BuildRecallGroup builds a group message recall: GroupRecallMsg{ Type(1)=1, GroupUin(2),
// Field3(3){ Sequence(1) }, Field4(4){ Field1(1)=0 } }.
func BuildRecallGroup(groupUin, seq uint32) []byte {
	return Marshal(
		VarintField(1, 1),
		VarintField(2, uint64(groupUin)),
		BytesField(3, Marshal(VarintField(1, uint64(seq)), VarintField(2, 0), VarintField(3, 0))),
		BytesField(4, Marshal(VarintField(1, 0))),
	)
}

// BuildRecallC2C builds a friend message recall: C2CRecallMsg{ Type(1)=1, TargetUid(3),
// Info(4){ ClientSequence(1), Random(2), MessageId(3), Timestamp(4), Field5(5)=0,
// MessageSequence(6) }, Settings(5){}, Field6(6)=0 }.
func BuildRecallC2C(targetUID string, seq, random, clientSeq, timestamp uint32) []byte {
	info := Marshal(
		VarintField(1, uint64(clientSeq)),
		VarintField(2, uint64(random)),
		VarintField(3, 0x10000000<<32|uint64(random)),
		VarintField(4, uint64(timestamp)),
		VarintField(5, 0),
		VarintField(6, uint64(seq)),
	)
	return Marshal(
		VarintField(1, 1),
		StringField(3, targetUID),
		BytesField(4, info),
	)
}

// --- Oidb actions ---

// BuildMuteMember mutes (or, with duration 0, unmutes) a member: 0x1253_1
// { GroupUin(1), Type(2)=1, Body(3){ TargetUid(1), Duration(2) } }.
func BuildMuteMember(groupUin uint32, targetUID string, duration uint32) ([]byte, string) {
	body := Marshal(VarintField(1, uint64(groupUin)), VarintField(2, 1),
		BytesField(3, Marshal(StringField(1, targetUID), VarintField(2, uint64(duration)))))
	return buildOidb(0x1253, 1, body), OidbCommand(0x1253, 1)
}

// BuildMuteGroup mutes or unmutes the whole group: 0x89a_0 { GroupUin(1), State(2){ S(17) } }
// where S is 0xFFFFFFFF to mute and 0 to lift.
func BuildMuteGroup(groupUin uint32, mute bool) ([]byte, string) {
	var s uint64
	if mute {
		s = 0xFFFFFFFF
	}
	body := Marshal(VarintField(1, uint64(groupUin)), BytesField(2, Marshal(VarintField(17, s))))
	return buildOidb(0x89a, 0, body), OidbCommand(0x89a, 0)
}

// BuildKickMember removes a member: 0x8a0_1 { GroupUin(1), TargetUid(3), RejectAddRequest(4),
// Field5(5)="" }.
func BuildKickMember(groupUin uint32, targetUID string, rejectAddRequest bool) ([]byte, string) {
	body := Marshal(VarintField(1, uint64(groupUin)), StringField(3, targetUID),
		VarintField(4, boolVarint(rejectAddRequest)))
	return buildOidb(0x8a0, 1, body), OidbCommand(0x8a0, 1)
}

// BuildSetAdmin grants or revokes a member's admin: 0x1096_1 { GroupUin(1), Uid(2), IsAdmin(3) }.
func BuildSetAdmin(groupUin uint32, targetUID string, admin bool) ([]byte, string) {
	body := Marshal(VarintField(1, uint64(groupUin)), StringField(2, targetUID),
		VarintField(3, boolVarint(admin)))
	return buildOidb(0x1096, 1, body), OidbCommand(0x1096, 1)
}

// BuildSetMemberCard sets a member's group card: 0x8fc_3 { GroupUin(1), Body(3){ TargetUid(1),
// TargetName(8) } }.
func BuildSetMemberCard(groupUin uint32, targetUID, name string) ([]byte, string) {
	body := Marshal(VarintField(1, uint64(groupUin)),
		BytesField(3, Marshal(StringField(1, targetUID), StringField(8, name))))
	return buildOidb(0x8fc, 3, body), OidbCommand(0x8fc, 3)
}

// BuildSetGroupName sets the group's name: 0x89a_15 { GroupUin(1), Body(2){ TargetName(3) } }.
func BuildSetGroupName(groupUin uint32, name string) ([]byte, string) {
	body := Marshal(VarintField(1, uint64(groupUin)), BytesField(2, Marshal(StringField(3, name))))
	return buildOidb(0x89a, 15, body), OidbCommand(0x89a, 15)
}

// BuildLeaveGroup quits a group: 0x1097_1 { GroupUin(1) }.
func BuildLeaveGroup(groupUin uint32) ([]byte, string) {
	return buildOidb(0x1097, 1, Marshal(VarintField(1, uint64(groupUin)))), OidbCommand(0x1097, 1)
}

// BuildSpecialTitle sets a member's special title: 0x8fc_2 { GroupUin(1), Body(3){ TargetUid(1),
// SpecialTitle(5), SpecialTitleExpireTime(6)=-1, UinName(7) } }.
func BuildSpecialTitle(groupUin uint32, targetUID, title string) ([]byte, string) {
	body := Marshal(VarintField(1, uint64(groupUin)),
		BytesField(3, Marshal(StringField(1, targetUID), StringField(5, title),
			VarintField(6, ^uint64(0)), StringField(7, title))))
	return buildOidb(0x8fc, 2, body), OidbCommand(0x8fc, 2)
}

// BuildPokeGroup pokes a member in a group: 0xed3_1 { Uin(1), GroupUin(2), Ext(6)=0 }. The
// target is named by uin, not uid.
func BuildPokeGroup(groupUin, targetUin uint32) ([]byte, string) {
	body := Marshal(VarintField(1, uint64(targetUin)), VarintField(2, uint64(groupUin)), VarintField(6, 0))
	return buildOidb(0xed3, 1, body), OidbCommand(0xed3, 1)
}

// BuildPokeFriend pokes a friend: 0xed3_1 { Uin(1), FriendUin(5), Ext(6)=0 }.
func BuildPokeFriend(targetUin uint32) ([]byte, string) {
	body := Marshal(VarintField(1, uint64(targetUin)), VarintField(5, uint64(targetUin)), VarintField(6, 0))
	return buildOidb(0xed3, 1, body), OidbCommand(0xed3, 1)
}

// BuildDeleteFriend removes a friend: 0x126b_0 { Field1{ TargetUid(1), Field2(2){130,109,{8,8,50}},
// Block(3), Field4(4)=true } }. The nested Field2 constants are what NT clients send; they are
// not derived locally. The target is named by uid.
func BuildDeleteFriend(targetUID string, block bool) ([]byte, string) {
	field2 := Marshal(VarintField(1, 130), VarintField(2, 109),
		BytesField(3, Marshal(VarintField(1, 8), VarintField(2, 8), VarintField(3, 50))))
	body := Marshal(BytesField(1, Marshal(
		StringField(1, targetUID),
		BytesField(2, field2),
		VarintField(3, boolVarint(block)),
		VarintField(4, 1),
	)))
	return buildOidb(0x126b, 0, body), OidbCommand(0x126b, 0)
}

func boolVarint(b bool) uint64 {
	if b {
		return 1
	}
	return 0
}

// BuildSetGroupRemark sets the local remark for a group: 0xf16_1
// { Body(1){ GroupUin(1), TargetRemark(3) } }.
func BuildSetGroupRemark(groupUin uint32, remark string) ([]byte, string) {
	body := Marshal(BytesField(1, Marshal(
		VarintField(1, uint64(groupUin)),
		StringField(3, remark),
	)))
	return buildOidb(0xf16, 1, body), OidbCommand(0xf16, 1)
}

// BuildSetEssence sets or clears a group essence message: 0xeac_1 to set, 0xeac_2 to
// delete. Body is { GroupUin(1), Sequence(2), Random(3) }.
func BuildSetEssence(groupUin, seq, random uint32, set bool) ([]byte, string) {
	sub := uint32(2)
	if set {
		sub = 1
	}
	body := Marshal(
		VarintField(1, uint64(groupUin)),
		VarintField(2, uint64(seq)),
		VarintField(3, uint64(random)),
	)
	return buildOidb(0xeac, sub, body), OidbCommand(0xeac, sub)
}

// BuildSetGroupReaction adds (0x9082_1) or removes (0x9082_2) a group message
// reaction. Type is 1 for a face id of at most three characters and 2 otherwise.
func BuildSetGroupReaction(groupUin, seq uint32, code string, add bool) ([]byte, string) {
	sub := uint32(2)
	if add {
		sub = 1
	}
	kind := uint64(1)
	if len(code) > 3 {
		kind = 2
	}
	body := Marshal(
		VarintField(2, uint64(groupUin)),
		VarintField(3, uint64(seq)),
		StringField(4, code),
		VarintField(5, kind),
	)
	return buildOidbReserved(0x9082, sub, body, 1), OidbCommand(0x9082, sub)
}

// BuildAtAllRemain asks how many @all mentions remain: 0x8a7_0
// { SubCmd(1)=1, LimitIntervalTypeForUin(2)=2, LimitIntervalTypeForGroup(3)=1,
// Uin(4), GroupUin(5) }. LagrangeGo addresses this one by UID (reserved=1).
func BuildAtAllRemain(selfUin, groupUin uint32) ([]byte, string) {
	body := Marshal(
		VarintField(1, 1),
		VarintField(2, 2),
		VarintField(3, 1),
		VarintField(4, uint64(selfUin)),
		VarintField(5, uint64(groupUin)),
	)
	return buildOidbReserved(0x8a7, 0, body, 1), OidbCommand(0x8a7, 0)
}

// AtAllRemain is the 0x8a7_0 reply.
type AtAllRemain struct {
	CanAtAll      bool
	CountForUin   uint32
	CountForGroup uint32
}

// ParseAtAllRemain reads CanAtAll(1), CountForUin(2), CountForGroup(3).
func ParseAtAllRemain(answer []byte) (AtAllRemain, error) {
	body, err := oidbSuccessBody(answer)
	if err != nil {
		return AtAllRemain{}, err
	}
	f, err := EveryField(body)
	if err != nil {
		return AtAllRemain{}, fmt.Errorf("protocol: at-all remain: %w", err)
	}
	return AtAllRemain{
		CanAtAll:      pbU32(f, 1) != 0,
		CountForUin:   pbU32(f, 2),
		CountForGroup: pbU32(f, 3),
	}, nil
}

// urlCheckQUA is the Qua string LagrangeGo sends with 0xbcb_0. It is copied from that
// builder, not derived from this client's own login QUA.
const urlCheckQUA = "AQQ_2013 4.6/2013 8.4.184945&NA_0/000000&ADR&null18&linux&2017&C2293D02BEE31158&7.1.2&V3"

// BuildURLCheck asks whether a URL is safe to open: 0xbcb_0 { CheckUrlReq(10) }.
func BuildURLCheck(selfUin uint32, url string) ([]byte, string) {
	req := Marshal(
		StringField(1, url),
		StringField(4, "mqq.group"),
		VarintField(5, 2),
		VarintField(9, uint64(selfUin)),
		StringField(10, "webview"),
		StringField(11, url),
		VarintField(12, 0),
		VarintField(14, 0),
		StringField(15, url),
		VarintField(16, 1),
		StringField(17, urlCheckQUA),
	)
	return buildOidb(0xbcb, 0, Marshal(BytesField(10, req))), OidbCommand(0xbcb, 0)
}

const (
	// URLSafe / URLUnknown / URLDanger match LagrangeGo's URLSecurityLevel (iota+1).
	URLSafe    = 1
	URLUnknown = 2
	URLDanger  = 3
)

// ParseURLCheck reads CheckUrlRsp(10).Results(1)[0]: a JumpUrl(4) is dangerous,
// UmrType(7)=2 is safe, anything else is unknown.
func ParseURLCheck(answer []byte) (int, error) {
	body, err := oidbSuccessBody(answer)
	if err != nil {
		return URLUnknown, err
	}
	f, err := EveryField(body)
	if err != nil {
		return URLUnknown, fmt.Errorf("protocol: url check: %w", err)
	}
	rsp, err := EveryField(pbBytes(f, 10))
	if err != nil {
		return URLUnknown, fmt.Errorf("protocol: url check: %w", err)
	}
	results := FieldsWith(rsp, 1)
	if len(results) == 0 || !results[0].IsData {
		return URLUnknown, fmt.Errorf("protocol: url check: empty result")
	}
	item, err := EveryField(results[0].Data)
	if err != nil {
		return URLUnknown, err
	}
	if _, ok := FirstField(item, 4); ok {
		return URLDanger, nil
	}
	if pbU32(item, 7) == 2 {
		return URLSafe, nil
	}
	return URLUnknown, nil
}

// BuildGroupSign clocks in for a group: 0xeb7_1 { SignInWriteReq(2){ Uin(1), GroupUin(2),
// AppVersion(3) } }. Uin and group are decimal strings, matching LagrangeGo.
func BuildGroupSign(selfUin, groupUin uint32, appVersion string) ([]byte, string) {
	req := Marshal(
		StringField(1, itoa(selfUin)),
		StringField(2, itoa(groupUin)),
		StringField(3, appVersion),
	)
	return buildOidb(0xeb7, 1, Marshal(BytesField(2, req))), OidbCommand(0xeb7, 1)
}

// GroupSign is the 0xeb7_1 DoneInfo.
type GroupSign struct {
	Title       string
	KeepDayText string
	GroupRank   string
	ClockInTime string
	DetailURL   string
}

// ParseGroupSign reads SignInWriteRsp(2).DoneInfo(2).
func ParseGroupSign(answer []byte) (GroupSign, error) {
	body, err := oidbSuccessBody(answer)
	if err != nil {
		return GroupSign{}, err
	}
	f, err := EveryField(body)
	if err != nil {
		return GroupSign{}, fmt.Errorf("protocol: group sign: %w", err)
	}
	rsp, err := EveryField(pbBytes(f, 2))
	if err != nil {
		return GroupSign{}, fmt.Errorf("protocol: group sign: %w", err)
	}
	done, err := EveryField(pbBytes(rsp, 2))
	if err != nil {
		return GroupSign{}, fmt.Errorf("protocol: group sign: %w", err)
	}
	out := GroupSign{
		Title:       pbStr(done, 1),
		KeepDayText: pbStr(done, 2),
		DetailURL:   pbStr(done, 4),
	}
	for i, info := range FieldsWith(done, 3) {
		if i == 0 {
			out.GroupRank = string(info.Data)
		}
		if i == 1 {
			out.ClockInTime = string(info.Data)
		}
	}
	if out.Title == "" && out.KeepDayText == "" {
		return GroupSign{}, fmt.Errorf("protocol: group sign: empty done info")
	}
	return out, nil
}

func oidbSuccessBody(answer []byte) ([]byte, error) {
	if err := ParseOidbReply(answer); err != nil {
		return nil, err
	}
	f, err := EveryField(stripSSOBodyLength(answer))
	if err != nil {
		return nil, err
	}
	return pbBytes(f, 4), nil
}
