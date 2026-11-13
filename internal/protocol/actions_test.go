package protocol

import "testing"

func TestOidbCommandFormat(t *testing.T) {
	cases := map[string][2]uint32{
		"OidbSvcTrpcTcp.0x89a_0":  {0x89a, 0},
		"OidbSvcTrpcTcp.0x1253_1": {0x1253, 1},
		"OidbSvcTrpcTcp.0x8a0_1":  {0x8a0, 1},
		"OidbSvcTrpcTcp.0x8fc_3":  {0x8fc, 3},
		"OidbSvcTrpcTcp.0xed3_1":  {0xed3, 1},
		"OidbSvcTrpcTcp.0x89a_15": {0x89a, 15},
	}
	for want, cs := range cases {
		if got := OidbCommand(cs[0], cs[1]); got != want {
			t.Errorf("OidbCommand(0x%x,%d) = %q, want %q", cs[0], cs[1], got, want)
		}
	}
}

// decodeOidb unwraps buildOidb for a test: returns command, service, inner body.
func decodeOidb(t *testing.T, packet []byte) (uint32, uint32, []byte) {
	t.Helper()
	f, err := EveryField(packet)
	if err != nil {
		t.Fatalf("decode oidb: %v", err)
	}
	return pbU32(f, 1), pbU32(f, 2), pbBytes(f, 4)
}

func TestBuildMuteMember(t *testing.T) {
	packet, cmd := BuildMuteMember(10000, "u_target", 600)
	if cmd != "OidbSvcTrpcTcp.0x1253_1" {
		t.Fatalf("cmd %q", cmd)
	}
	c, s, body := decodeOidb(t, packet)
	if c != 0x1253 || s != 1 {
		t.Fatalf("cmd/sub %x/%d", c, s)
	}
	bf, _ := EveryField(body)
	if pbU32(bf, 1) != 10000 || pbU32(bf, 2) != 1 {
		t.Fatalf("group/type: %+v", bf)
	}
	inner, _ := EveryField(pbBytes(bf, 3))
	if pbStr(inner, 1) != "u_target" || pbU32(inner, 2) != 600 {
		t.Fatalf("uid/duration: %+v", inner)
	}
}

func TestBuildMuteGroup(t *testing.T) {
	packet, _ := BuildMuteGroup(10000, true)
	_, _, body := decodeOidb(t, packet)
	bf, _ := EveryField(body)
	state, _ := EveryField(pbBytes(bf, 2))
	if pbU64(state, 17) != 0xFFFFFFFF {
		t.Fatalf("mute S = %x, want 0xFFFFFFFF", pbU64(state, 17))
	}
	packet2, _ := BuildMuteGroup(10000, false)
	_, _, body2 := decodeOidb(t, packet2)
	bf2, _ := EveryField(body2)
	state2, _ := EveryField(pbBytes(bf2, 2))
	if pbU64(state2, 17) != 0 {
		t.Fatalf("unmute S = %x, want 0", pbU64(state2, 17))
	}
}

func TestBuildKickMember(t *testing.T) {
	packet, _ := BuildKickMember(10000, "u_k", true)
	_, _, body := decodeOidb(t, packet)
	bf, _ := EveryField(body)
	if pbU32(bf, 1) != 10000 || pbStr(bf, 3) != "u_k" || pbU32(bf, 4) != 1 {
		t.Fatalf("fields: %+v", bf)
	}
}

func TestBuildSetMemberCardAndTitle(t *testing.T) {
	// card: 0x8fc_3, TargetName at field 8.
	packet, cmd := BuildSetMemberCard(1, "u_m", "新名片")
	if cmd != "OidbSvcTrpcTcp.0x8fc_3" {
		t.Fatalf("cmd %q", cmd)
	}
	_, _, body := decodeOidb(t, packet)
	bf, _ := EveryField(body)
	inner, _ := EveryField(pbBytes(bf, 3))
	if pbStr(inner, 1) != "u_m" || pbStr(inner, 8) != "新名片" {
		t.Fatalf("card fields: %+v", inner)
	}
	// title: 0x8fc_2, SpecialTitle at field 5, expire at field 6 = -1.
	packet2, cmd2 := BuildSpecialTitle(1, "u_m", "头衔")
	if cmd2 != "OidbSvcTrpcTcp.0x8fc_2" {
		t.Fatalf("cmd %q", cmd2)
	}
	_, _, body2 := decodeOidb(t, packet2)
	bf2, _ := EveryField(body2)
	inner2, _ := EveryField(pbBytes(bf2, 3))
	if pbStr(inner2, 5) != "头衔" || pbU64(inner2, 6) != ^uint64(0) {
		t.Fatalf("title fields: %+v", inner2)
	}
}

func TestBuildSetGroupName(t *testing.T) {
	packet, cmd := BuildSetGroupName(10000, "新群名")
	if cmd != "OidbSvcTrpcTcp.0x89a_15" {
		t.Fatalf("cmd %q", cmd)
	}
	_, _, body := decodeOidb(t, packet)
	bf, _ := EveryField(body)
	inner, _ := EveryField(pbBytes(bf, 2))
	if pbStr(inner, 3) != "新群名" {
		t.Fatalf("name at field 3: %+v", inner)
	}
}

func TestBuildPoke(t *testing.T) {
	// group poke: Uin(1), GroupUin(2).
	packet, cmd := BuildPokeGroup(10000, 123)
	if cmd != "OidbSvcTrpcTcp.0xed3_1" {
		t.Fatalf("cmd %q", cmd)
	}
	_, _, body := decodeOidb(t, packet)
	bf, _ := EveryField(body)
	if pbU32(bf, 1) != 123 || pbU32(bf, 2) != 10000 {
		t.Fatalf("group poke: %+v", bf)
	}
	// friend poke: Uin(1), FriendUin(5).
	packet2, _ := BuildPokeFriend(456)
	_, _, body2 := decodeOidb(t, packet2)
	bf2, _ := EveryField(body2)
	if pbU32(bf2, 1) != 456 || pbU32(bf2, 5) != 456 {
		t.Fatalf("friend poke: %+v", bf2)
	}
}

func TestBuildDeleteFriend(t *testing.T) {
	packet, cmd := BuildDeleteFriend("u_gone", true)
	if cmd != "OidbSvcTrpcTcp.0x126b_0" {
		t.Fatalf("cmd %q", cmd)
	}
	_, _, body := decodeOidb(t, packet)
	outer, _ := EveryField(body)
	inner, _ := EveryField(pbBytes(outer, 1))
	if pbStr(inner, 1) != "u_gone" || pbU32(inner, 3) != 1 || pbU32(inner, 4) != 1 {
		t.Fatalf("delete friend fields: %+v", inner)
	}
	field2, _ := EveryField(pbBytes(inner, 2))
	if pbU32(field2, 1) != 130 || pbU32(field2, 2) != 109 {
		t.Fatalf("delete friend field2: %+v", field2)
	}
}

func TestBuildRecallGroup(t *testing.T) {
	body := BuildRecallGroup(10000, 777)
	f, _ := EveryField(body)
	if pbU32(f, 1) != 1 || pbU32(f, 2) != 10000 {
		t.Fatalf("type/group: %+v", f)
	}
	inner, _ := EveryField(pbBytes(f, 3))
	if pbU32(inner, 1) != 777 {
		t.Fatalf("seq: %+v", inner)
	}
}

func TestParseOidbReply(t *testing.T) {
	ok := ssoBodyWrap(Marshal(VarintField(1, 0x1253), VarintField(3, 0)))
	if err := ParseOidbReply(ok); err != nil {
		t.Fatalf("code 0 should be ok: %v", err)
	}
	bad := ssoBodyWrap(Marshal(VarintField(1, 0x1253), VarintField(3, 42), StringField(5, "no permission")))
	if err := ParseOidbReply(bad); err == nil {
		t.Fatalf("code 42 should error")
	}
}

func TestBuildSetGroupRemarkAndEssence(t *testing.T) {
	packet, cmd := BuildSetGroupRemark(90001, "lab")
	if cmd != "OidbSvcTrpcTcp.0xf16_1" {
		t.Fatal(cmd)
	}
	_, _, body := decodeOidb(t, packet)
	wrap, _ := EveryField(body)
	inner, _ := EveryField(pbBytes(wrap, 1))
	if pbU32(inner, 1) != 90001 || pbStr(inner, 3) != "lab" {
		t.Fatalf("%+v", inner)
	}
	set, setCmd := BuildSetEssence(90001, 12, 99, true)
	if setCmd != "OidbSvcTrpcTcp.0xeac_1" {
		t.Fatal(setCmd)
	}
	_, _, body = decodeOidb(t, set)
	f, _ := EveryField(body)
	if pbU32(f, 1) != 90001 || pbU32(f, 2) != 12 || pbU32(f, 3) != 99 {
		t.Fatalf("%+v", f)
	}
	_, delCmd := BuildSetEssence(90001, 12, 99, false)
	if delCmd != "OidbSvcTrpcTcp.0xeac_2" {
		t.Fatal(delCmd)
	}
	react, reactCmd := BuildSetGroupReaction(90001, 12, "76", true)
	if reactCmd != "OidbSvcTrpcTcp.0x9082_1" {
		t.Fatal(reactCmd)
	}
	outer, _ := EveryField(react)
	if pbU64(outer, 12) != 1 {
		t.Fatalf("reserved %d", pbU64(outer, 12))
	}
	_, _, body = decodeOidb(t, react)
	f, _ = EveryField(body)
	if pbU32(f, 2) != 90001 || pbU32(f, 3) != 12 || pbStr(f, 4) != "76" || pbU32(f, 5) != 1 {
		t.Fatalf("%+v", f)
	}
	_, unsetCmd := BuildSetGroupReaction(90001, 12, "[NO]", false)
	if unsetCmd != "OidbSvcTrpcTcp.0x9082_2" {
		t.Fatal(unsetCmd)
	}
}

func TestBuildAtAllRemainAndURLCheckAndSign(t *testing.T) {
	packet, cmd := BuildAtAllRemain(10001, 90001)
	if cmd != "OidbSvcTrpcTcp.0x8a7_0" {
		t.Fatal(cmd)
	}
	outer, _ := EveryField(packet)
	if pbU64(outer, 12) != 1 {
		t.Fatalf("reserved %d", pbU64(outer, 12))
	}
	_, _, body := decodeOidb(t, packet)
	f, _ := EveryField(body)
	if pbU32(f, 1) != 1 || pbU32(f, 2) != 2 || pbU32(f, 3) != 1 || pbU64(f, 4) != 10001 || pbU64(f, 5) != 90001 {
		t.Fatalf("%+v", f)
	}
	remain, err := ParseAtAllRemain(ssoBodyWrap(Marshal(
		VarintField(1, 0x8a7), VarintField(3, 0),
		BytesField(4, Marshal(VarintField(1, 1), VarintField(2, 3), VarintField(3, 4))),
	)))
	if err != nil || !remain.CanAtAll || remain.CountForUin != 3 || remain.CountForGroup != 4 {
		t.Fatal(remain, err)
	}

	up, upCmd := BuildURLCheck(10001, "https://example.com")
	if upCmd != "OidbSvcTrpcTcp.0xbcb_0" {
		t.Fatal(upCmd)
	}
	_, _, body = decodeOidb(t, up)
	wrap, _ := EveryField(body)
	req, _ := EveryField(pbBytes(wrap, 10))
	if pbStr(req, 1) != "https://example.com" || pbU32(req, 5) != 2 || pbU64(req, 9) != 10001 {
		t.Fatalf("%+v", req)
	}
	level, err := ParseURLCheck(ssoBodyWrap(Marshal(
		VarintField(1, 0xbcb), VarintField(3, 0),
		BytesField(4, Marshal(BytesField(10, Marshal(BytesField(1, Marshal(VarintField(7, 2))))))),
	)))
	if err != nil || level != URLSafe {
		t.Fatal(level, err)
	}
	danger, err := ParseURLCheck(ssoBodyWrap(Marshal(
		VarintField(1, 0xbcb), VarintField(3, 0),
		BytesField(4, Marshal(BytesField(10, Marshal(BytesField(1, Marshal(StringField(4, "http://bad"))))))),
	)))
	if err != nil || danger != URLDanger {
		t.Fatal(danger, err)
	}

	sign, signCmd := BuildGroupSign(10001, 90001, "7.0.2")
	if signCmd != "OidbSvcTrpcTcp.0xeb7_1" {
		t.Fatal(signCmd)
	}
	_, _, body = decodeOidb(t, sign)
	signWrap, _ := EveryField(body)
	req2, _ := EveryField(pbBytes(signWrap, 2))
	if pbStr(req2, 1) != "10001" || pbStr(req2, 2) != "90001" || pbStr(req2, 3) != "7.0.2" {
		t.Fatalf("%+v", req2)
	}
	got, err := ParseGroupSign(ssoBodyWrap(Marshal(
		VarintField(1, 0xeb7), VarintField(3, 0),
		BytesField(4, Marshal(BytesField(2, Marshal(BytesField(2, Marshal(
			StringField(1, "今日已成功打卡"), StringField(2, "已打卡3天"),
			StringField(3, "群内排名第1位"), StringField(4, "https://qun.qq.com/v2/signin/detail"),
		)))))),
	)))
	if err != nil || got.Title != "今日已成功打卡" || got.KeepDayText != "已打卡3天" || got.GroupRank != "群内排名第1位" {
		t.Fatal(got, err)
	}
}

// ssoBodyWrap prefixes the 4-byte big-endian length an SSO reply body carries.
func ssoBodyWrap(body []byte) []byte {
	out := make([]byte, 4+len(body))
	n := uint32(len(body) + 4)
	out[0], out[1], out[2], out[3] = byte(n>>24), byte(n>>16), byte(n>>8), byte(n)
	copy(out[4:], body)
	return out
}
