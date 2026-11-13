package protocol

import "testing"

func TestBuildGroupFilePackets(t *testing.T) {
	list, cmd := BuildGroupFileList(90001, "/", 0, 20)
	if cmd != "OidbSvcTrpcTcp.0x6d8_1" {
		t.Fatal(cmd)
	}
	outer, _ := EveryField(list)
	if pbU64(outer, 12) != 1 {
		t.Fatalf("reserved %d", pbU64(outer, 12))
	}
	_, _, body := decodeOidb(t, list)
	wrap, _ := EveryField(body)
	inner, _ := EveryField(pbBytes(wrap, 2))
	if pbU32(inner, 1) != 90001 || pbStr(inner, 3) != "/" || pbU32(inner, 5) != 20 {
		t.Fatalf("%+v", inner)
	}

	_, countCmd := BuildGroupFileCount(90001)
	if countCmd != "OidbSvcTrpcTcp.0x6d8_2" {
		t.Fatal(countCmd)
	}
	_, spaceCmd := BuildGroupFileSpace(90001)
	if spaceCmd != "OidbSvcTrpcTcp.0x6d8_3" {
		t.Fatal(spaceCmd)
	}
	dl, dlCmd := BuildGroupFileDownload(90001, "file-1")
	if dlCmd != "OidbSvcTrpcTcp.0x6d6_2" {
		t.Fatal(dlCmd)
	}
	_, _, body = decodeOidb(t, dl)
	wrap, _ = EveryField(body)
	inner, _ = EveryField(pbBytes(wrap, 3))
	if pbU32(inner, 1) != 90001 || pbStr(inner, 4) != "file-1" {
		t.Fatalf("%+v", inner)
	}
	_, delCmd := BuildGroupFileDelete(90001, "file-1", 0)
	if delCmd != "OidbSvcTrpcTcp.0x6d6_3" {
		t.Fatal(delCmd)
	}
	_, mkdirCmd := BuildGroupFolderCreate(90001, "/", "docs")
	if mkdirCmd != "OidbSvcTrpcTcp.0x6d7_0" {
		t.Fatal(mkdirCmd)
	}
	_, rmdirCmd := BuildGroupFolderDelete(90001, "/folder")
	if rmdirCmd != "OidbSvcTrpcTcp.0x6d7_1" {
		t.Fatal(rmdirCmd)
	}
	rn, rnCmd := BuildGroupFileRename(90001, "file-1", "/", "b.txt", 0)
	if rnCmd != "OidbSvcTrpcTcp.0x6d6_4" {
		t.Fatal(rnCmd)
	}
	_, _, body = decodeOidb(t, rn)
	wrap, _ = EveryField(body)
	inner, _ = EveryField(pbBytes(wrap, 5))
	if pbStr(inner, 4) != "file-1" || pbStr(inner, 6) != "b.txt" || pbU32(inner, 3) != 102 {
		t.Fatalf("%+v", inner)
	}
	mv, mvCmd := BuildGroupFileMove(90001, "file-1", "/", "/docs", 0)
	if mvCmd != "OidbSvcTrpcTcp.0x6d6_5" {
		t.Fatal(mvCmd)
	}
	_, _, body = decodeOidb(t, mv)
	wrap, _ = EveryField(body)
	inner, _ = EveryField(pbBytes(wrap, 6))
	if pbStr(inner, 5) != "/" || pbStr(inner, 6) != "/docs" {
		t.Fatalf("%+v", inner)
	}
	_, folderRnCmd := BuildGroupFolderRename(90001, "/folder", "docs")
	if folderRnCmd != "OidbSvcTrpcTcp.0x6d7_2" {
		t.Fatal(folderRnCmd)
	}
	send, sendCmd := BuildGroupSendFile(90001, "file-1", 9)
	if sendCmd != "OidbSvcTrpcTcp.0x6d9_4" {
		t.Fatal(sendCmd)
	}
	_, _, body = decodeOidb(t, send)
	wrap, _ = EveryField(body)
	inner, _ = EveryField(pbBytes(wrap, 5))
	info, _ := EveryField(pbBytes(inner, 3))
	if pbU32(inner, 1) != 90001 || pbU32(inner, 2) != 4 || pbU32(info, 1) != 102 || pbStr(info, 2) != "file-1" || pbU32(info, 3) != 9 || pbU32(info, 5) != 1 {
		t.Fatalf("%+v %+v", inner, info)
	}
}

func TestParseGroupFileListAndDownload(t *testing.T) {
	file := Marshal(
		StringField(1, "fid"), StringField(2, "a.txt"), VarintField(3, 12),
		VarintField(4, 102), VarintField(6, 10), VarintField(15, 123), StringField(14, "n"),
	)
	folder := Marshal(StringField(1, "/f"), StringField(3, "docs"), VarintField(6, 123))
	page, err := ParseGroupFileList(ssoBodyWrap(Marshal(
		VarintField(1, 0x6d8), VarintField(3, 0),
		BytesField(4, Marshal(BytesField(2, Marshal(
			VarintField(1, 0), VarintField(4, 1),
			BytesField(5, Marshal(VarintField(1, 2), BytesField(3, file))),
			BytesField(5, Marshal(VarintField(1, 1), BytesField(2, folder))),
		)))),
	)))
	if err != nil || !page.IsEnd || len(page.Files) != 1 || page.Files[0].FileName != "a.txt" || len(page.Folders) != 1 {
		t.Fatal(page, err)
	}
	url, err := ParseGroupFileDownload(ssoBodyWrap(Marshal(
		VarintField(1, 0x6d6), VarintField(3, 0),
		BytesField(4, Marshal(BytesField(3, Marshal(
			VarintField(1, 0), StringField(4, "10.0.0.1"), BytesField(6, []byte{0xab}),
		)))),
	)))
	if err != nil || url != "https://10.0.0.1:443/ftn_handler/ab/?fname=" {
		t.Fatal(url, err)
	}
	n, limit, err := ParseGroupFileCount(ssoBodyWrap(Marshal(
		VarintField(1, 0x6d8), VarintField(3, 0),
		BytesField(4, Marshal(BytesField(3, Marshal(VarintField(4, 3), VarintField(6, 100))))),
	)))
	if err != nil || n != 3 || limit != 100 {
		t.Fatal(n, limit, err)
	}
}

func TestGroupFileStatisticsRejectBusinessErrors(t *testing.T) {
	for _, field := range []int{3, 4} {
		answer := Marshal(VarintField(3, 0), BytesField(4, Marshal(BytesField(field, Marshal(VarintField(1, 5), StringField(3, "access denied"))))))
		var err error
		if field == 3 {
			_, _, err = ParseGroupFileCount(answer)
		} else {
			_, _, err = ParseGroupFileSpace(answer)
		}
		if err == nil {
			t.Fatal("business error reported as zero statistics")
		}
	}
	if _, err := ParseGroupFileList(Marshal(VarintField(3, 0))); err == nil {
		t.Fatal("missing list accepted as empty")
	}
}
func TestGroupFileDownloadUsesRequestedBusID(t *testing.T) {
	packet, _ := BuildGroupFileDownload(90001, "id", 107)
	outer, _ := EveryField(packet)
	body, _ := EveryField(pbBytes(outer, 4))
	req, _ := EveryField(pbBytes(body, 3))
	if pbU32(req, 3) != 107 {
		t.Fatal("busid ignored")
	}
}

func TestGroupFileDownloadPrefersTLSHostname(t *testing.T) {
	answer := Marshal(VarintField(3, 0), BytesField(4, Marshal(BytesField(3, Marshal(StringField(4, "223.109.208.142"), StringField(5, "download.ftn.qq.com"), BytesField(6, []byte{0xab}))))))
	got, err := ParseGroupFileDownload(answer)
	if err != nil || got != "https://download.ftn.qq.com:443/ftn_handler/ab/?fname=" {
		t.Fatalf("%s %v", got, err)
	}
}
