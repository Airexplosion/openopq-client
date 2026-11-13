package protocol

import "testing"

func TestSendForwardRoundTrip(t *testing.T) {
	nodes := []ForwardNode{{
		SenderUin: 123, SenderUid: "u_s", SenderName: "n",
		Time: 1700000000, Sequence: 12,
		Elements: [][]byte{TextElem("hello")},
	}}
	pkt := BuildSendForward("u_self", 90001, nodes)
	top, err := EveryField(pkt)
	if err != nil {
		t.Fatal(err)
	}
	info, _ := EveryField(pbBytes(top, 2))
	if pbU32(info, 1) != 3 || pbU32(info, 3) != 90001 {
		t.Fatalf("type/group %+v", info)
	}
	uid, _ := EveryField(pbBytes(info, 2))
	if pbStr(uid, 2) != "90001" {
		t.Fatalf("uid slot %q", pbStr(uid, 2))
	}
	raw, err := limitedInflate(pbBytes(info, 4), true)
	if err != nil {
		t.Fatal(err)
	}
	actions, _ := EveryField(raw)
	action, _ := EveryField(pbBytes(actions, 2))
	if pbStr(action, 1) != "MultiMsg" {
		t.Fatal(pbStr(action, 1))
	}
	content, _ := EveryField(pbBytes(action, 2))
	msgs := FieldsWith(content, 1)
	if len(msgs) != 1 {
		t.Fatal(len(msgs))
	}
	mf, _ := EveryField(msgs[0].Data)
	m := decodeMessageBody(mf)
	if !m.IsGroup || m.GroupUin != 90001 || m.SenderUin != 123 || m.SenderNick != "n" || m.Content != "hello" || m.MsgSeq != 12 {
		t.Fatalf("%+v", m)
	}

	friend := BuildSendForward("u_self", 0, nodes)
	ft, _ := EveryField(friend)
	fi, _ := EveryField(pbBytes(ft, 2))
	if pbU32(fi, 1) != 1 {
		t.Fatalf("friend type %d", pbU32(fi, 1))
	}
	fuid, _ := EveryField(pbBytes(fi, 2))
	if pbStr(fuid, 2) != "u_self" {
		t.Fatal(pbStr(fuid, 2))
	}

	resid, err := ParseSendForward(ssoBodyWrap(Marshal(BytesField(2, Marshal(StringField(3, "resid-1"))))))
	if err != nil || resid != "resid-1" {
		t.Fatal(resid, err)
	}
	elem := ForwardLightAppElem("resid-1", nodes)
	ef, _ := EveryField(elem)
	if _, ok := FirstField(ef, 51); !ok {
		t.Fatal("missing light-app elem")
	}
}

func TestDecodeContentHeadRandom(t *testing.T) {
	body := Marshal(
		BytesField(1, Marshal(VarintField(1, 123), BytesField(8, Marshal(VarintField(1, 90001))))),
		BytesField(2, Marshal(VarintField(1, 82), VarintField(4, 99), VarintField(5, 12), VarintField(6, 1))),
		BytesField(3, Marshal(BytesField(1, Marshal(BytesField(2, TextElem("x")))))),
	)
	f, _ := EveryField(body)
	m := decodeMessageBody(f)
	if m.MsgRandom != 99 || m.MsgSeq != 12 {
		t.Fatalf("%+v", m)
	}
}
