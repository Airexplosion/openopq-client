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
	return Marshal(
		VarintField(1, uint64(command)),
		VarintField(2, uint64(service)),
		BytesField(4, body),
		VarintField(12, 0),
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

func boolVarint(b bool) uint64 {
	if b {
		return 1
	}
	return 0
}
