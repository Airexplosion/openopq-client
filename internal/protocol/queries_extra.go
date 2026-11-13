package protocol

import (
	"fmt"
	"strconv"
)

// Extra OneBot queries copied from LagrangeGo: fetch rkey (0x9067_202),
// image OCR (0xe07_0) and group details (0x88d_0).

// fetchGroupAppID is the AppID LagrangeGo sends on 0x88d_0. It is copied
// from that builder, not derived from this client's login AppID.
const fetchGroupAppID = 537099973

// BuildFetchRKey asks 0x9067_202 for NT download rkeys of types 10, 20 and 2.
func BuildFetchRKey() ([]byte, string) {
	head := Marshal(
		BytesField(1, Marshal(VarintField(1, 1), VarintField(2, 202))),
		BytesField(2, Marshal(VarintField(101, 2), VarintField(102, 1), VarintField(200, 0))),
		BytesField(3, Marshal(VarintField(1, 2))),
	)
	req := Marshal(VarintField(1, 10), VarintField(1, 20), VarintField(1, 2))
	return buildOidb(0x9067, 202, Marshal(BytesField(1, head), BytesField(4, req))), OidbCommand(0x9067, 202)
}

// RKey is one NT download rkey.
type RKey struct {
	RKey       string
	TTL        uint64
	CreateTime uint32
	Type       uint32
}

// ParseFetchRKey reads DownloadRKeyResp.RKeys.
func ParseFetchRKey(answer []byte) ([]RKey, error) {
	body, err := oidbSuccessBody(answer)
	if err != nil {
		return nil, err
	}
	top, err := EveryField(body)
	if err != nil {
		return nil, fmt.Errorf("protocol: rkey: %w", err)
	}
	rsp, err := EveryField(pbBytes(top, 4))
	if err != nil {
		return nil, fmt.Errorf("protocol: rkey: %w", err)
	}
	var out []RKey
	for _, item := range FieldsWith(rsp, 1) {
		if !item.IsData {
			continue
		}
		f, err := EveryField(item.Data)
		if err != nil {
			return nil, err
		}
		out = append(out, RKey{
			RKey:       pbStr(f, 1),
			TTL:        pbU64(f, 2),
			CreateTime: pbU32(f, 4),
			Type:       pbU32(f, 5),
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("protocol: rkey: empty")
	}
	return out, nil
}

// BuildImageOCR asks 0xe07_0 to read text from an image QQ can fetch.
func BuildImageOCR(imageURL string) ([]byte, string) {
	body := Marshal(
		VarintField(1, 1),
		VarintField(2, 0),
		VarintField(3, 1),
		BytesField(10, Marshal(StringField(1, imageURL))),
	)
	return buildOidbReserved(0xe07, 0, body, 1), OidbCommand(0xe07, 0)
}

// OCRText is one detected line.
type OCRText struct {
	Text        string
	Confidence  int32
	Coordinates [][2]int32
}

// OCRResult is the 0xe07_0 reply.
type OCRResult struct {
	Texts    []OCRText
	Language string
}

// ParseImageOCR reads OcrRspBody.
func ParseImageOCR(answer []byte) (OCRResult, error) {
	body, err := oidbSuccessBody(answer)
	if err != nil {
		return OCRResult{}, err
	}
	top, err := EveryField(body)
	if err != nil {
		return OCRResult{}, fmt.Errorf("protocol: ocr: %w", err)
	}
	if wording := pbStr(top, 3); wording != "" {
		return OCRResult{}, fmt.Errorf("protocol: ocr: %s", wording)
	}
	if code := pbU32(top, 1); code != 0 {
		return OCRResult{}, fmt.Errorf("protocol: ocr: code %d %s", code, pbStr(top, 2))
	}
	rsp, err := EveryField(pbBytes(top, 10))
	if err != nil {
		return OCRResult{}, fmt.Errorf("protocol: ocr: %w", err)
	}
	out := OCRResult{Language: pbStr(rsp, 2)}
	for _, det := range FieldsWith(rsp, 1) {
		if !det.IsData {
			continue
		}
		d, err := EveryField(det.Data)
		if err != nil {
			return OCRResult{}, err
		}
		line := OCRText{Text: pbStr(d, 1), Confidence: int32(pbU32(d, 2))}
		poly, _ := EveryField(pbBytes(d, 3))
		for _, c := range FieldsWith(poly, 1) {
			if !c.IsData {
				continue
			}
			pt, err := EveryField(c.Data)
			if err != nil {
				return OCRResult{}, err
			}
			line.Coordinates = append(line.Coordinates, [2]int32{int32(pbU32(pt, 1)), int32(pbU32(pt, 2))})
		}
		out.Texts = append(out.Texts, line)
	}
	return out, nil
}

// BuildFetchGroup asks 0x88d_0 for one group's details.
func BuildFetchGroup(groupUin uint32) ([]byte, string) {
	info := Marshal(
		VarintField(1, 1),
		VarintField(2, 1),
		VarintField(5, 1),
		VarintField(6, 1),
		VarintField(10, 1),
		StringField(15, ""),
		StringField(17, ""),
		VarintField(21, 1),
		VarintField(22, 1),
		VarintField(23, 1),
		StringField(24, ""),
		StringField(25, ""),
		VarintField(45, 1),
		VarintField(46, 1),
	)
	body := Marshal(
		VarintField(1, fetchGroupAppID),
		BytesField(2, Marshal(VarintField(1, uint64(groupUin)), BytesField(2, info))),
	)
	return buildOidb(0x88d, 0, body), OidbCommand(0x88d, 0)
}

// GroupDetail is the 0x88d_0 reply we expose to OneBot.
type GroupDetail struct {
	GroupUin       uint32
	Owner          string
	CreateTime     uint32
	MaxMemberCount uint32
	MemberCount    uint32
	Level          uint32
	Name           string
	Memo           string
	FingerMemo     string
	CurMsgSeq      uint32
	LastMsgTime    uint32
	Question       string
	Answer         string
	ShutupAll      uint32
	ShutupMe       uint32
}

// ParseFetchGroup reads RspGroupInfo.groupInfo.
func ParseFetchGroup(answer []byte) (GroupDetail, error) {
	body, err := oidbSuccessBody(answer)
	if err != nil {
		return GroupDetail{}, err
	}
	top, err := EveryField(body)
	if err != nil {
		return GroupDetail{}, fmt.Errorf("protocol: fetch group: %w", err)
	}
	infoWrap, err := EveryField(pbBytes(top, 1))
	if err != nil {
		return GroupDetail{}, fmt.Errorf("protocol: fetch group: %w", err)
	}
	if code := pbU32(infoWrap, 2); code != 0 {
		return GroupDetail{}, fmt.Errorf("protocol: fetch group: result %d", code)
	}
	g, err := EveryField(pbBytes(infoWrap, 3))
	if err != nil {
		return GroupDetail{}, fmt.Errorf("protocol: fetch group: %w", err)
	}
	out := GroupDetail{
		GroupUin:       pbU32(infoWrap, 1),
		Owner:          pbStr(g, 1),
		CreateTime:     pbU32(g, 2),
		MaxMemberCount: pbU32(g, 5),
		MemberCount:    pbU32(g, 6),
		Level:          pbU32(g, 10),
		Name:           pbStr(g, 15),
		Memo:           pbStr(g, 16),
		FingerMemo:     pbStr(g, 17),
		CurMsgSeq:      pbU32(g, 22),
		LastMsgTime:    pbU32(g, 23),
		Question:       pbStr(g, 24),
		Answer:         pbStr(g, 25),
		ShutupAll:      pbU32(g, 45),
		ShutupMe:       pbU32(g, 46),
	}
	if out.GroupUin == 0 {
		out.GroupUin = pbU32(g, 21)
	}
	if out.Name == "" && out.GroupUin == 0 {
		return GroupDetail{}, fmt.Errorf("protocol: fetch group: empty")
	}
	return out, nil
}

// OwnerUin parses GroupOwner when QQ sent a decimal uin.
func (g GroupDetail) OwnerUin() uint64 {
	n, err := strconv.ParseUint(g.Owner, 10, 32)
	if err != nil {
		return 0
	}
	return n
}

// privateFileDownloadTrailer is the hardcoded 90200:1 trailer LagrangeGo sends
// on 0xe37_1200 as field 99999.
var privateFileDownloadTrailer = []byte{0xc0, 0x85, 0x2c, 0x01}

// BuildPrivateFileDownload asks 0xe37_1200 for a C2C offline-file URL.
func BuildPrivateFileDownload(selfUID, fileUUID, fileHash string) ([]byte, string) {
	body := Marshal(
		VarintField(1, 1200),
		VarintField(2, 1),
		BytesField(14, Marshal(
			StringField(10, selfUID),
			StringField(20, fileUUID),
			VarintField(30, 2),
			StringField(60, fileHash),
		)),
		VarintField(101, 3),
		VarintField(102, 103),
		VarintField(200, 1),
		BytesField(99999, privateFileDownloadTrailer),
	)
	return buildOidb(0xe37, 1200, body), OidbCommand(0xe37, 1200)
}

// ParsePrivateFileDownload reads the 0xe37_1200 download URL.
func ParsePrivateFileDownload(answer []byte) (string, error) {
	body, err := oidbSuccessBody(answer)
	if err != nil {
		return "", err
	}
	top, err := EveryField(body)
	if err != nil {
		return "", fmt.Errorf("protocol: private file: %w", err)
	}
	inner, err := EveryField(pbBytes(top, 14))
	if err != nil {
		return "", fmt.Errorf("protocol: private file: %w", err)
	}
	result, err := EveryField(pbBytes(inner, 30))
	if err != nil {
		return "", fmt.Errorf("protocol: private file: %w", err)
	}
	host := pbStr(result, 20)
	path := pbStr(result, 50)
	if host == "" || path == "" {
		return "", fmt.Errorf("protocol: private file: empty url")
	}
	return fmt.Sprintf("http://%s:%d%s&isthumb=0", host, pbU32(result, 40), path), nil
}
