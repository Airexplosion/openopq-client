package protocol

import "testing"

func TestBuildAndParseRKeyOCRAndGroup(t *testing.T) {
	pkt, cmd := BuildFetchRKey()
	if cmd != "OidbSvcTrpcTcp.0x9067_202" {
		t.Fatal(cmd)
	}
	_, _, body := decodeOidb(t, pkt)
	top, _ := EveryField(body)
	req, _ := EveryField(pbBytes(top, 4))
	var types []uint32
	for _, f := range FieldsWith(req, 1) {
		types = append(types, uint32(f.Number))
	}
	if len(types) != 3 || types[0] != 10 || types[2] != 2 {
		t.Fatal(types)
	}
	keys, err := ParseFetchRKey(ssoBodyWrap(Marshal(
		VarintField(1, 0x9067), VarintField(3, 0),
		BytesField(4, Marshal(BytesField(4, Marshal(BytesField(1, Marshal(
			StringField(1, "&rkey=x"), VarintField(2, 3600), VarintField(4, 1), VarintField(5, 10),
		)))))),
	)))
	if err != nil || len(keys) != 1 || keys[0].RKey != "&rkey=x" || keys[0].Type != 10 {
		t.Fatal(keys, err)
	}

	ocrPkt, ocrCmd := BuildImageOCR("https://gchat.qpic.cn/a")
	if ocrCmd != "OidbSvcTrpcTcp.0xe07_0" {
		t.Fatal(ocrCmd)
	}
	outer, _ := EveryField(ocrPkt)
	if pbU64(outer, 12) != 1 {
		t.Fatalf("reserved %d", pbU64(outer, 12))
	}
	got, err := ParseImageOCR(ssoBodyWrap(Marshal(
		VarintField(1, 0xe07), VarintField(3, 0),
		BytesField(4, Marshal(
			VarintField(1, 0),
			BytesField(10, Marshal(
				BytesField(1, Marshal(StringField(1, "hi"), VarintField(2, 90))),
				StringField(2, "zh"),
			)),
		)),
	)))
	if err != nil || got.Language != "zh" || len(got.Texts) != 1 || got.Texts[0].Text != "hi" {
		t.Fatal(got, err)
	}

	gPkt, gCmd := BuildFetchGroup(90001)
	if gCmd != "OidbSvcTrpcTcp.0x88d_0" {
		t.Fatal(gCmd)
	}
	_, _, body = decodeOidb(t, gPkt)
	wrap, _ := EveryField(body)
	if pbU32(wrap, 1) != fetchGroupAppID {
		t.Fatal(pbU32(wrap, 1))
	}
	cfg, _ := EveryField(pbBytes(wrap, 2))
	if pbU32(cfg, 1) != 90001 {
		t.Fatal(cfg)
	}
	detail, err := ParseFetchGroup(ssoBodyWrap(Marshal(
		VarintField(1, 0x88d), VarintField(3, 0),
		BytesField(4, Marshal(BytesField(1, Marshal(
			VarintField(1, 90001), VarintField(2, 0),
			BytesField(3, Marshal(StringField(1, "123"), StringField(15, "lab"), VarintField(6, 4))),
		)))),
	)))
	if err != nil || detail.Name != "lab" || detail.OwnerUin() != 123 || detail.MemberCount != 4 {
		t.Fatal(detail, err)
	}

	priv, privCmd := BuildPrivateFileDownload("u_self", "uuid-1", "hash-1")
	if privCmd != "OidbSvcTrpcTcp.0xe37_1200" {
		t.Fatal(privCmd)
	}
	_, _, body = decodeOidb(t, priv)
	inner, _ := EveryField(body)
	if pbU32(inner, 1) != 1200 || string(pbBytes(inner, 99999)) != string(privateFileDownloadTrailer) {
		t.Fatalf("%+v", inner)
	}
	privReq, _ := EveryField(pbBytes(inner, 14))
	if pbStr(privReq, 10) != "u_self" || pbStr(privReq, 20) != "uuid-1" || pbStr(privReq, 60) != "hash-1" {
		t.Fatalf("%+v", privReq)
	}
	url, err := ParsePrivateFileDownload(ssoBodyWrap(Marshal(
		VarintField(1, 0xe37), VarintField(3, 0),
		BytesField(4, Marshal(BytesField(14, Marshal(
			BytesField(30, Marshal(StringField(20, "10.0.0.1"), VarintField(40, 80), StringField(50, "/dl?x=1"))),
		)))),
	)))
	if err != nil || url != "http://10.0.0.1:80/dl?x=1&isthumb=0" {
		t.Fatal(url, err)
	}
}
