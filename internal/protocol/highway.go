package protocol

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strconv"
)

// This file carries the NT V2 rich-media (image/voice) send: the Highway-session request
// (HttpConn.0x6ff_501), the Oidb upload request/response (NTV2RichMedia), the Highway transfer
// framing, and the CommonElem the uploaded media is sent as. Structures and field numbers are
// read off LagrangeGo (client/richmedia.go, packets/oidb, packets/pb/service/{oidb,highway}).

// --- Highway session (servers + session ticket) ---

const HighwayURLCommand = "HttpConn.0x6ff_501"

// HighwayServer is one Highway upload endpoint.
type HighwayServer struct {
	IP   uint32
	Port uint32
}

// Addr formats the server as host:port. The IP integer is little-endian, matching the client.
func (s HighwayServer) Addr() string {
	return fmt.Sprintf("%d.%d.%d.%d:%d",
		byte(s.IP), byte(s.IP>>8), byte(s.IP>>16), byte(s.IP>>24), s.Port)
}

// HighwaySession is what a transfer needs: the servers to dial and the session ticket.
type HighwaySession struct {
	SigSession []byte
	SessionKey []byte
	Servers    []HighwayServer
}

// BuildHighwayURLReq builds the HttpConn.0x6ff_501 request. The HttpConn message sits in the
// outer field 1281; its contents are the fixed values the client sends plus the login Tgt.
func BuildHighwayURLReq(tgt []byte) []byte {
	httpConn := Marshal(
		VarintField(1, 0), VarintField(2, 0), VarintField(3, 16), VarintField(4, 1),
		StringField(5, hex.EncodeToString(tgt)),
		VarintField(6, 3),
		VarintField(7, 1), VarintField(7, 5), VarintField(7, 10), VarintField(7, 21),
		VarintField(9, 2), VarintField(10, 9), VarintField(11, 8),
		StringField(15, "1.0.1"),
	)
	return Marshal(BytesField(1281, httpConn))
}

// ParseHighwayURLResp reads the HttpConn.0x6ff_501 reply into a session: the SigSession, the
// SessionKey and the ServiceType-1 servers.
func ParseHighwayURLResp(payload []byte) (HighwaySession, error) {
	payload = stripSSOBodyLength(payload)
	top, err := EveryField(payload)
	if err != nil {
		return HighwaySession{}, fmt.Errorf("protocol: highway url: %w", err)
	}
	wrap, ok := FirstField(top, 1281)
	if !ok || !wrap.IsData {
		return HighwaySession{}, fmt.Errorf("protocol: highway url: no response body")
	}
	resp, err := EveryField(wrap.Data)
	if err != nil {
		return HighwaySession{}, err
	}
	session := HighwaySession{SigSession: pbBytes(resp, 1), SessionKey: pbBytes(resp, 2)}
	for _, info := range FieldsWith(resp, 3) { // ServerInfo
		if !info.IsData {
			continue
		}
		sf, _ := EveryField(info.Data)
		if pbU32(sf, 1) != 1 { // ServiceType 1 = the image/media servers
			continue
		}
		for _, addr := range FieldsWith(sf, 2) { // ServerAddr
			if !addr.IsData {
				continue
			}
			af, _ := EveryField(addr.Data)
			session.Servers = append(session.Servers, HighwayServer{IP: pbU32(af, 2), Port: pbU32(af, 3)})
		}
	}
	return session, nil
}

// --- media metadata ---

// MediaMeta describes an image or voice file for its upload request.
type MediaMeta struct {
	Size   uint32
	Md5    []byte
	Sha1   []byte
	Width  uint32 // image
	Height uint32 // image
	Format uint32 // PicFormat enum for an image
	Ext    string // file extension ("jpg"/"png"/"gif"/"amr")
	// voice
	Duration uint32
	IsVoice  bool
}

// Image format enum values, matching LagrangeGo's utils/img.
const (
	picJpeg = 1000
	picPng  = 1001
	picWebp = 1002
	picBmp  = 1005
	picGif  = 2000
)

// ResolveImage reads an image's format and dimensions from its header (PNG, JPEG, GIF; others
// fall back to JPEG with unknown dimensions).
func ResolveImage(data []byte) (format uint32, ext string, width, height uint32) {
	switch {
	case len(data) >= 24 && data[0] == 0x89 && data[1] == 'P' && data[2] == 'N' && data[3] == 'G':
		return picPng, "png", binary.BigEndian.Uint32(data[16:20]), binary.BigEndian.Uint32(data[20:24])
	case len(data) >= 10 && data[0] == 'G' && data[1] == 'I' && data[2] == 'F':
		return picGif, "gif", uint32(binary.LittleEndian.Uint16(data[6:8])), uint32(binary.LittleEndian.Uint16(data[8:10]))
	case len(data) >= 2 && data[0] == 0xFF && data[1] == 0xD8:
		w, h := jpegSize(data)
		return picJpeg, "jpg", w, h
	case len(data) >= 2 && data[0] == 'B' && data[1] == 'M':
		if len(data) >= 26 {
			return picBmp, "bmp", binary.LittleEndian.Uint32(data[18:22]), binary.LittleEndian.Uint32(data[22:26])
		}
	case len(data) >= 12 && data[8] == 'W' && data[9] == 'E' && data[10] == 'B' && data[11] == 'P':
		return picWebp, "webp", 0, 0
	}
	return picJpeg, "jpg", 0, 0
}

// jpegSize scans JPEG segments for a start-of-frame marker and reads the dimensions from it.
func jpegSize(data []byte) (uint32, uint32) {
	for i := 2; i+9 < len(data); {
		if data[i] != 0xFF {
			i++
			continue
		}
		marker := data[i+1]
		// SOF markers carry the dimensions; C4/C8/CC are not frame headers.
		if marker >= 0xC0 && marker <= 0xCF && marker != 0xC4 && marker != 0xC8 && marker != 0xCC {
			h := binary.BigEndian.Uint16(data[i+5 : i+7])
			w := binary.BigEndian.Uint16(data[i+7 : i+9])
			return uint32(w), uint32(h)
		}
		if marker == 0xD8 || marker == 0xD9 || (marker >= 0xD0 && marker <= 0xD7) {
			i += 2
			continue
		}
		seg := binary.BigEndian.Uint16(data[i+2 : i+4])
		i += 2 + int(seg)
	}
	return 0, 0
}

// --- upload request / response ---

// ImageUpload is the result of an image upload request: the Highway credentials (empty UKey
// means the file already exists — no transfer), the servers it names, and the MsgInfo to send.
type ImageUpload struct {
	UKey        string
	Servers     []HighwayServer // from the oidb reply; carried in the Highway ext
	FileUUID    string
	FileSha1    []byte
	MsgInfo     []byte   // the whole MsgInfo, re-sent as the CommonElem payload
	MsgInfoBody [][]byte // the MsgInfoBody entries, carried in the Highway ext
	CompatQMsg  []byte   // the pre-NT element (CustomFace for a group, NotOnlineImage for c2c)
}

// BuildImageUploadReq builds the NTV2RichMedia upload request for a group (targetUID empty) or a
// private image. It is Oidb 0x11c4_100 for a group, 0x11c5_100 for c2c.
func BuildImageUploadReq(groupUin uint32, targetUID string, meta MediaMeta, tempGroup ...uint32) ([]byte, string) {
	isGroup := targetUID == ""
	var scene []Field
	scene = append(scene, VarintField(101, 2), VarintField(102, 1)) // RequestType=2, BusinessType=1 (image)
	if isGroup {
		scene = append(scene, VarintField(200, 2), BytesField(202, Marshal(VarintField(1, uint64(groupUin)))))
	} else {
		scene = append(scene, VarintField(200, 1), BytesField(201, mediaPrivateScene(targetUID, tempGroup)))
	}
	reqHead := Marshal(
		BytesField(1, Marshal(VarintField(1, 1), VarintField(2, 100))), // CommonHead{RequestId,Command}
		BytesField(2, Marshal(scene...)),                               // SceneInfo
		BytesField(3, Marshal(VarintField(1, 2))),                      // ClientMeta{AgentType:2}
	)

	fileType := Marshal(VarintField(1, 1), VarintField(2, uint64(meta.Format))) // Type=1(image), PicFormat
	fileInfo := Marshal(
		VarintField(1, uint64(meta.Size)),
		StringField(2, hex.EncodeToString(meta.Md5)),
		StringField(3, hex.EncodeToString(meta.Sha1)),
		StringField(4, hex.EncodeToString(meta.Md5)+"."+meta.Ext),
		BytesField(5, fileType),
		VarintField(6, uint64(meta.Width)), VarintField(7, uint64(meta.Height)),
		VarintField(8, 0), VarintField(9, 1),
	)
	uploadInfo := Marshal(BytesField(1, fileInfo), VarintField(2, 0))
	extBiz := Marshal(BytesField(1, Marshal( // Pic
		VarintField(1, 0), StringField(2, "[图片]"),
		BytesField(12, Marshal(VarintField(1, 0), StringField(9, "[图片]"))), // ExtData
	)))
	compatScene := uint64(1)
	if isGroup {
		compatScene = 2
	}
	upload := Marshal(
		BytesField(1, uploadInfo),
		VarintField(2, 1), // TryFastUploadCompleted
		VarintField(3, 0), // SrvSendMsg
		VarintField(4, uint64(randU64Media())),
		VarintField(5, compatScene),
		BytesField(6, extBiz),
		VarintField(8, 0),
	)
	body := Marshal(BytesField(1, reqHead), BytesField(2, upload))
	if isGroup {
		return buildOidb(0x11c4, 100, body), OidbCommand(0x11c4, 100)
	}
	return buildOidb(0x11c5, 100, body), OidbCommand(0x11c5, 100)
}

// BuildVoiceUploadReq builds the NTV2RichMedia upload request for a group (targetUID empty) or a
// private voice message. Oidb 0x126e_100 (group) / 0x126d_100 (c2c). The voice is expected to be
// SILK/AMR-encoded already; duration is in seconds.
func BuildVoiceUploadReq(groupUin uint32, targetUID string, meta MediaMeta, tempGroup ...uint32) ([]byte, string) {
	isGroup := targetUID == ""
	var scene []Field
	scene = append(scene, VarintField(101, 2), VarintField(102, 3)) // RequestType=2, BusinessType=3 (voice)
	compatScene := uint64(1)
	if isGroup {
		scene = append(scene, VarintField(200, 2), BytesField(202, Marshal(VarintField(1, uint64(groupUin)))))
		compatScene = 2
	} else {
		scene = append(scene, VarintField(200, 1), BytesField(201, mediaPrivateScene(targetUID, tempGroup)))
	}
	reqHead := Marshal(
		BytesField(1, Marshal(VarintField(1, 1), VarintField(2, 100))),
		BytesField(2, Marshal(scene...)),
		BytesField(3, Marshal(VarintField(1, 2))),
	)
	fileType := Marshal(VarintField(1, 3), VarintField(4, 1)) // Type=3 (voice), VoiceFormat=1
	fileInfo := Marshal(
		VarintField(1, uint64(meta.Size)),
		StringField(2, hex.EncodeToString(meta.Md5)),
		StringField(3, hex.EncodeToString(meta.Sha1)),
		StringField(4, hex.EncodeToString(meta.Md5)+".amr"),
		BytesField(5, fileType),
		VarintField(6, 0), VarintField(7, 0),
		VarintField(8, uint64(meta.Duration)), VarintField(9, 0),
	)
	uploadInfo := Marshal(BytesField(1, fileInfo), VarintField(2, 0))
	// Ptt ExtBizInfo with the generation's magic bytes (BytesReserve 11, BytesPbReserve 12,
	// BytesGeneralFlags 13), different for group and private.
	var ptt []Field
	if isGroup {
		ptt = []Field{
			BytesField(12, []byte{0x08, 0x00, 0x38, 0x00}),
			BytesField(13, []byte{0x9a, 0x01, 0x07, 0xaa, 0x03, 0x04, 0x08, 0x08, 0x12, 0x00}),
		}
	} else {
		ptt = []Field{
			BytesField(11, []byte{0x08, 0x00, 0x38, 0x00}),
			BytesField(13, []byte{0x9a, 0x01, 0x0b, 0xaa, 0x03, 0x08, 0x08, 0x04, 0x12, 0x04, 0x00, 0x00, 0x00, 0x00}),
		}
	}
	extBiz := Marshal(BytesField(3, Marshal(ptt...)))
	upload := Marshal(
		BytesField(1, uploadInfo),
		VarintField(2, 1), VarintField(3, 0),
		VarintField(4, uint64(randU64Media())),
		VarintField(5, compatScene),
		BytesField(6, extBiz),
		VarintField(8, 0),
	)
	body := Marshal(BytesField(1, reqHead), BytesField(2, upload))
	if isGroup {
		return buildOidb(0x126e, 100, body), OidbCommand(0x126e, 100)
	}
	return buildOidb(0x126d, 100, body), OidbCommand(0x126d, 100)
}

// VoiceCommonElem builds the CommonElem an uploaded voice is sent as: service type 48, the
// MsgInfo payload, business type 22. A voice carries no pre-NT compat element.
func VoiceCommonElem(msgInfo []byte) []byte {
	return Marshal(BytesField(53, Marshal(
		VarintField(1, 48), BytesField(2, msgInfo), VarintField(3, 22),
	)))
}

// ParseImageUploadResp reads an NTV2RichMedia upload reply.
func ParseImageUploadResp(answer []byte) (ImageUpload, error) {
	answer = stripSSOBodyLength(answer)
	base, err := EveryField(answer)
	if err != nil {
		return ImageUpload{}, fmt.Errorf("protocol: image upload: %w", err)
	}
	if code := pbU32(base, 3); code != 0 {
		return ImageUpload{}, fmt.Errorf("protocol: image upload refused: code=%d %s", code, pbStr(base, 5))
	}
	respBody := pbBytes(base, 4)
	resp, err := EveryField(respBody)
	if err != nil {
		return ImageUpload{}, err
	}
	up, ok := FirstField(resp, 2) // UploadResp
	if !ok || !up.IsData {
		return ImageUpload{}, fmt.Errorf("protocol: image upload: no upload response")
	}
	uf, _ := EveryField(up.Data)
	out := ImageUpload{
		UKey:       pbStr(uf, 1),
		CompatQMsg: pbBytes(uf, 8),
	}
	for _, ip := range FieldsWith(uf, 3) { // IPv4
		if !ip.IsData {
			continue
		}
		ipf, _ := EveryField(ip.Data)
		out.Servers = append(out.Servers, HighwayServer{IP: pbU32(ipf, 1), Port: pbU32(ipf, 2)})
	}
	if mi, ok := FirstField(uf, 6); ok && mi.IsData { // MsgInfo
		out.MsgInfo = mi.Data
		info, _ := EveryField(mi.Data)
		for _, body := range FieldsWith(info, 1) { // MsgInfoBody
			if body.IsData {
				out.MsgInfoBody = append(out.MsgInfoBody, body.Data)
			}
		}
		if len(out.MsgInfoBody) > 0 {
			body0, _ := EveryField(out.MsgInfoBody[0])
			if idx, ok := FirstField(body0, 1); ok && idx.IsData { // IndexNode
				idxf, _ := EveryField(idx.Data)
				out.FileUUID = pbStr(idxf, 2)                       // FileUuid
				if fi, ok := FirstField(idxf, 1); ok && fi.IsData { // FileInfo
					fif, _ := EveryField(fi.Data)
					out.FileSha1 = decodeHexMaybe(pbStr(fif, 3)) // FileSha1 (hex)
				}
			}
		}
	}
	return out, nil
}

// --- Highway transfer framing ---

// BuildHighwayExt builds the NTV2RichMediaHighwayExt that rides in each upload chunk's head
// (field 3), carrying the ukey, the servers and the MsgInfoBody.
func BuildHighwayExt(up ImageUpload, blockSize uint32) []byte {
	var ipv4s []Field
	for _, s := range up.Servers {
		domain := Marshal(VarintField(1, 1), StringField(2, fmt.Sprintf("%d.%d.%d.%d",
			byte(s.IP), byte(s.IP>>8), byte(s.IP>>16), byte(s.IP>>24))))
		ipv4s = append(ipv4s, BytesField(1, Marshal(BytesField(1, domain), VarintField(2, uint64(s.Port)))))
	}
	fields := []Field{
		StringField(1, up.FileUUID),
		StringField(2, up.UKey),
		BytesField(5, Marshal(ipv4s...)), // Network{ IPv4S }
	}
	for _, body := range up.MsgInfoBody {
		fields = append(fields, BytesField(6, body)) // MsgInfoBody
	}
	fields = append(fields,
		VarintField(10, uint64(blockSize)),
		BytesField(11, Marshal(BytesField(1, up.FileSha1))), // Hash{ FileSha1 }
	)
	return Marshal(fields...)
}

// BuildHighwayHead builds the ReqDataHighwayHead for one chunk.
func BuildHighwayHead(uin, appID, subAppID, seq, commandID uint32, fileSize uint64,
	offset uint64, chunkLen uint32, chunkMd5, fileMd5, ticket, loginSig, ext []byte) []byte {
	base := Marshal(
		VarintField(1, 1), // Version
		StringField(2, strconv.FormatUint(uint64(uin), 10)),
		StringField(3, "PicUp.DataUp"),
		VarintField(4, uint64(seq)),
		VarintField(5, 0), // RetryTimes
		VarintField(6, uint64(subAppID)),
		VarintField(7, 16), // DataFlag
		VarintField(8, uint64(commandID)),
	)
	seg := Marshal(
		VarintField(1, 0), // ServiceId
		VarintField(2, fileSize),
		VarintField(3, offset),
		VarintField(4, uint64(chunkLen)),
		VarintField(5, 0), // RetCode
		BytesField(6, ticket),
		BytesField(8, chunkMd5),
		BytesField(9, fileMd5),
		VarintField(10, 0), VarintField(13, 0), // CacheAddr, CachePort
	)
	loginHead := Marshal(VarintField(1, 8), BytesField(2, loginSig), VarintField(3, uint64(appID)))
	fields := []Field{BytesField(1, base), BytesField(2, seg)}
	if len(ext) > 0 {
		fields = append(fields, BytesField(3, ext))
	}
	fields = append(fields, BytesField(5, loginHead))
	return Marshal(fields...)
}

// FrameHighway wraps a head and body in the Highway frame: 0x28, head length, body length, head,
// body, 0x29.
func FrameHighway(head, body []byte) []byte {
	out := make([]byte, 9, 9+len(head)+len(body)+1)
	out[0] = 0x28
	binary.BigEndian.PutUint32(out[1:], uint32(len(head)))
	binary.BigEndian.PutUint32(out[5:], uint32(len(body)))
	out = append(out, head...)
	out = append(out, body...)
	return append(out, 0x29)
}

// ParseHighwayRespHead reads a Highway reply's head and returns its error code.
func ParseHighwayRespHead(head []byte) (uint32, error) {
	f, err := EveryField(head)
	if err != nil {
		return 0, err
	}
	return pbU32(f, 3), nil // ErrorCode
}

// --- send elements ---

// ImageCommonElem builds the CommonElem (Elem field 53) an uploaded image is sent as: service
// type 48, the MsgInfo as the payload, and the business type (20 group / 10 private).
func ImageCommonElem(msgInfo []byte, isGroup bool) []byte {
	business := uint64(10)
	if isGroup {
		business = 20
	}
	return Marshal(BytesField(53, Marshal(
		VarintField(1, 48), BytesField(2, msgInfo), VarintField(3, business),
	)))
}

// ImageCompatElem builds the pre-NT image element from the upload's CompatQMsg: a CustomFace
// (Elem field 8) for a group, a NotOnlineImage (Elem field 4) for a private chat.
func ImageCompatElem(compatQMsg []byte, isGroup bool) []byte {
	if len(compatQMsg) == 0 {
		return nil
	}
	if isGroup {
		return Marshal(BytesField(8, compatQMsg))
	}
	return Marshal(BytesField(4, compatQMsg))
}

func randU64Media() uint64 {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0x1234567890
	}
	return binary.BigEndian.Uint64(b[:])
}
