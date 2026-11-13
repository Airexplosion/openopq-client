package protocol

import "testing"

func TestBuildReadedReport(t *testing.T) {
	group := BuildReadedReportGroup(90001, 44)
	f, _ := EveryField(group)
	inner, _ := EveryField(pbBytes(f, 1))
	if pbU32(inner, 1) != 90001 || pbU32(inner, 2) != 44 {
		t.Fatalf("group read report: %+v", inner)
	}
	c2c := BuildReadedReportC2C("u_peer", 1700000000, 9)
	f2, _ := EveryField(c2c)
	inner2, _ := EveryField(pbBytes(f2, 2))
	if pbStr(inner2, 2) != "u_peer" || pbU32(inner2, 3) != 1700000000 || pbU32(inner2, 4) != 9 {
		t.Fatalf("c2c read report: %+v", inner2)
	}
}

func TestBuildGetGroupAndRoamMsg(t *testing.T) {
	packet := BuildGetGroupMsg(90001, 10, 20)
	f, _ := EveryField(packet)
	info, _ := EveryField(pbBytes(f, 1))
	if pbU32(info, 1) != 90001 || pbU32(info, 2) != 10 || pbU32(info, 3) != 20 || pbU32(f, 2) != 1 {
		t.Fatalf("group history request: %+v / %+v", f, info)
	}
	roam := BuildGetRoamMsg("u_peer", 1700000000, 20)
	rf, _ := EveryField(roam)
	if pbStr(rf, 1) != "u_peer" || pbU32(rf, 2) != 1700000000 || pbU32(rf, 4) != 20 || pbU32(rf, 5) != 1 {
		t.Fatalf("roam request: %+v", rf)
	}
}

func historyPushBody(seq, sender, group uint32, text string) []byte {
	head := []Field{VarintField(1, uint64(sender)), StringField(2, "u_sender")}
	if group != 0 {
		head = append(head, BytesField(8, Marshal(VarintField(1, uint64(group)), StringField(4, "card"), StringField(7, "group"))))
	}
	content := Marshal(VarintField(5, uint64(seq)), VarintField(6, 1700000000))
	elem := Marshal(BytesField(1, Marshal(StringField(1, text))))
	body := Marshal(BytesField(1, Marshal(BytesField(2, elem))))
	return Marshal(BytesField(1, Marshal(head...)), BytesField(2, content), BytesField(3, body))
}

func TestParseGetGroupMsgResponse(t *testing.T) {
	msg := historyPushBody(12, 123, 90001, "hello")
	reply := ssoBodyWrap(Marshal(BytesField(3, Marshal(BytesField(6, msg)))))
	out, err := ParseGetGroupMsgResponse(reply)
	if err != nil || len(out) != 1 {
		t.Fatalf("parse: %v %+v", err, out)
	}
	if !out[0].IsGroup || out[0].GroupUin != 90001 || out[0].SenderUin != 123 || out[0].MsgSeq != 12 || out[0].Content != "hello" {
		t.Fatalf("decoded %+v", out[0])
	}
}

func TestParseGetRoamMsgResponse(t *testing.T) {
	msg := historyPushBody(8, 123, 0, "priv")
	reply := ssoBodyWrap(Marshal(BytesField(7, msg)))
	out, err := ParseGetRoamMsgResponse(reply)
	if err != nil || len(out) != 1 || out[0].IsGroup || out[0].SenderUin != 123 || out[0].Content != "priv" {
		t.Fatalf("parse: %v %+v", err, out)
	}
}
