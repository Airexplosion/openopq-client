package protocol

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"encoding/hex"
	"testing"
)

func TestOrderedRichContentAndModernVoice(t *testing.T) {
	node := Marshal(BytesField(1, Marshal(StringField(2, "0123456789abcdef0123456789abcdef"), StringField(4, "test.silk"))), StringField(2, "file-uuid"))
	voice := Marshal(BytesField(53, Marshal(VarintField(1, 48), BytesField(2, Marshal(BytesField(1, Marshal(BytesField(1, node))))), VarintField(3, 12))))
	body := Marshal(BytesField(1, Marshal(BytesField(1, Marshal(VarintField(1, 123))), BytesField(3, Marshal(BytesField(1, Marshal(BytesField(2, TextElem("before")), BytesField(2, voice), BytesField(2, TextElem("after")))))))))
	m, e := DecodePushMessage(body)
	if e != nil {
		t.Fatal(e)
	}
	if len(m.Images) != 0 || len(m.Segments) != 3 || m.Segments[1].Type != "record" || m.Segments[0].Data["text"] != "before" || m.Segments[2].Data["text"] != "after" {
		t.Fatalf("lost voice or order: %+v", m)
	}
	if !bytes.Equal(m.Segments[1].Media.Index, node) {
		t.Fatal("download locator lost")
	}
}

func TestGroupCardIsSeparateFromPersonalNickname(t *testing.T) {
	head := Marshal(VarintField(1, 456), BytesField(7, Marshal(StringField(6, "QQ昵称"))), BytesField(8, Marshal(VarintField(1, 90001), StringField(4, ".sn 群名片"))))
	m, err := DecodePushMessage(Marshal(BytesField(1, Marshal(BytesField(1, head)))))
	if err != nil || m.SenderNick != "QQ昵称" || m.GroupCard != ".sn 群名片" {
		t.Fatalf("mixed nickname and card: %+v, %v", m, err)
	}
}
func TestCompatImageKeepsModernDownloadIndex(t *testing.T) {
	hash := bytes.Repeat([]byte{1}, 16)
	node := Marshal(BytesField(1, Marshal(StringField(2, hex.EncodeToString(hash)))), StringField(2, "uuid"))
	common := Marshal(BytesField(53, Marshal(VarintField(1, 48), VarintField(3, 10), BytesField(2, Marshal(BytesField(1, Marshal(BytesField(1, node))))))))
	f := []Field{BytesField(3, Marshal(BytesField(1, Marshal(BytesField(2, Marshal(BytesField(4, Marshal(BytesField(7, hash), StringField(15, "/legacy"))))), BytesField(2, common)))))}
	m := decodeMessageBody(f)
	if len(m.Segments) != 1 || !bytes.Equal(m.Segments[0].Media.Index, node) {
		t.Fatal("compat element hid modern locator", m.Segments)
	}
}
func TestForwardDecompressionAndIdentity(t *testing.T) {
	node := Marshal(BytesField(1, Marshal(VarintField(1, 123), BytesField(7, Marshal(StringField(6, "测试昵称"))))), BytesField(2, Marshal(VarintField(6, 123456))), BytesField(3, Marshal(BytesField(1, Marshal(BytesField(2, TextElem("测试节点")))))))
	content := Marshal(BytesField(2, Marshal(StringField(1, "MultiMsg"), BytesField(2, Marshal(BytesField(1, node))))))
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	_, _ = w.Write(content)
	_ = w.Close()
	response := Marshal(BytesField(1, Marshal(StringField(3, "res-id"), BytesField(4, gz.Bytes()))))
	messages, e := ParseReceiveForward(response, "res-id")
	if e != nil || len(messages) != 1 || messages[0].SenderNick != "测试昵称" || messages[0].Content != "测试节点" {
		t.Fatal(messages, e)
	}
	if _, e = ParseReceiveForward(response, "other-id"); e == nil {
		t.Fatal("accepted wrong resource")
	}
	if _, e = ParseReceiveForward([]byte{255}, "id"); e == nil {
		t.Fatal("accepted malformed forward")
	}
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	_, _ = zw.Write([]byte(`<msg m_resid="test-res"/>`))
	_ = zw.Close()
	card := append([]byte{1}, z.Bytes()...)
	if forwardID(cardData(card), true) != "test-res" {
		t.Fatal("compressed XML forward not decoded")
	}
}
func TestRequestNoticeDecode(t *testing.T) {
	n := decodeNotice(528, 35, Marshal(BytesField(1, Marshal(StringField(1, "u_self"), StringField(2, "u_applicant"), StringField(10, "测试申请")))))
	if n == nil || n.Kind != NoticeFriendRequest || n.UserUid != "u_applicant" || n.Comment != "测试申请" {
		t.Fatal(n)
	}
	n = decodeNotice(87, 0, Marshal(VarintField(1, 90001), StringField(5, "u_invitor")))
	if n == nil || n.SubType != "invite" || n.RequestEvent != 2 {
		t.Fatal(n)
	}
}
