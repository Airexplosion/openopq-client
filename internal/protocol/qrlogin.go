package protocol

import (
	"bytes"
	"fmt"
	"os"
)

// The QR flow, as far as it has been recovered.
//
// Everything below was read out of a running client rather than inferred: the frame shape
// from a frame captured on the wire and from a second one found live in memory, and the
// names and field list from the client's own logging, which it writes to memory in the
// clear even though its traffic is enciphered.
//
// What is known:
//
//   - A QR code is fetched and then polled through wtlogin.trans_emp.
//   - The request body travels inside the OICQ frame, which BuildOICQ already produces:
//     the command field is 2066, and the frame carries a fresh nonce and a fresh
//     twenty-five-byte public key on every connection.
//   - The body opens with a record the client calls QRCodeDBHead and carries a sub-command:
//     49 to fetch, 18 to poll.
//   - A reply's body carries the QR record under TLV 0xD1, which the client calls
//     QRCodeTlv_209 and logs as a url plus device_type, device_name, tips and push_result.
//   - The reply also reports query_time and expire_time, and the code lapses after 120
//     seconds of the client's own clock.
//
// The key the body is enciphered with is not a constant and never was: the two sides agree
// one per connection, on the curve the protocol calls secp192k1, and the body travels under
// the MD5 of the agreed point's X coordinate. That is why a captured frame cannot be opened
// later and why nothing in memory was there to find — and it is also why the flow can be
// built here at all, since the agreement needs only the service's public key, which every
// client carries.
//
// The blocks a fetch carries are a transcription of the published implementation, one field
// per line, in the order it writes them. Two things in this file are not transcriptions and
// say so where they are: the code block's protobuf field numbers, which the file that named
// the block did not carry, and the state byte of a poll, whose values are not established
// here. Both are narrow enough to be settled by a fetch the service accepts.
//
// Published research:
//   - LagrangeDev/acidify, "登录认证与会话管理", WtLogin.kt and TlvQRCode.kt — the TransEmp
//     sub-commands, the curve, the shared key, and the layout of everything below
//   - LagrangeDev/Lagrange.Core and LagrangeGo — QR login in C# and Go
//   - ricq (Rust) — process_trans_emp_response and process_qrcode_confirmed

// TransEmpCommand is the service a QR code is fetched and polled through.
const TransEmpCommand = "wtlogin.trans_emp"

// The sub-commands the request body carries. The published implementations name the same
// two numbers, and the client's own log reports 49 for a fetch.
const (
	// QRSubCommandFetch asks for a QR code to be issued. 0x31.
	QRSubCommandFetch uint32 = 0x31
	// QRSubCommandPoll asks whether it has been scanned yet. 0x12, which is the number the
	// client's own log reported for a poll. A summary of the published implementations
	// reads 0x32 here, but that is the version code the record carries, not the sub-command.
	QRSubCommandPoll uint32 = 0x12
)

// The TLVs a fetch carries and the block a reply answers with, as the published
// implementations name them. The block holding the code is 0xD1, which the client's own log
// calls QRCodeTlv_209.
const (
	QRFetchTLVSession uint16 = 0x16
	QRFetchTLVDevice  uint16 = 0x1D
	QRCodeTag         uint16 = 0xD1
	// QRReplyImageTag carries the code as a PNG.
	QRReplyImageTag  uint16 = 0x17
	QRReplyURLExpiry        = 120
	// qrBlock16Version is the version string block 0x16 carries, which a 7.0.2 client's own
	// fetch writes as "2.0.0" — a constant of this block, not the client's own version.
	qrBlock16Version = "2.0.0"
	// qrFetchBitmap is the bitmap block 0x1d carries, read off a macOS 7.0.2 client's fetch.
	qrFetchBitmap uint32 = 0x7ffc
)

// The markers inside a request body.
const (
	// transEmpPacketStart opens the inner record and transEmpPacketEnd closes it.
	transEmpPacketStart = 0x02
	transEmpPacketEnd   = 0x03
	// transEmpHeadSize is the fixed part of the inner record that the head length counts,
	// measured from the start marker: everything before the TLVs, the account included.
	transEmpHeadSize = 49
	// transEmpVersionCode is a constant the record carries, written wide.
	transEmpVersionCode uint32 = 0x32
	// transEmpRole and transEmpPadding are constants of the layer around the record.
	transEmpRole     uint32 = 0x72
	transEmpKeyUsage byte   = 0
)

// TransEmpBody is a fetch or a poll before it is enciphered.
type TransEmpBody struct {
	SubCommand uint32
	AppID      uint32
	// SubAppID is the platform's half of the identity (OPQ's SubAppID, Lagrange's app_id). The
	// record's own layer and its 0x16 block carry it where a real 7.0.2 client's fetch does,
	// which is not where the per-build AppID goes.
	SubAppID  uint32
	Timestamp uint32
	// TLVs is the encoded TLV list the sub-command carries. What goes in it is the one part
	// of this flow that has not been read out of the published implementations, so it is an
	// input rather than something this builds.
	TLVs []byte
}

// buildTransEmpBody encodes the inner record.
//
// The head length counts from the start marker to the closing one, so it covers the fixed
// head, the TLVs and that closing byte.
func buildTransEmpBody(in TransEmpBody) []byte {
	body := make([]byte, 0, transEmpHeadSize+len(in.TLVs)+8)
	body = append(body, U32(in.Timestamp)...)
	body = append(body, transEmpPacketStart)
	body = append(body, U16(uint16(transEmpHeadSize+len(in.TLVs)+1))...)
	body = append(body, U16(uint16(in.SubCommand))...)
	body = append(body, make([]byte, 21)...)
	body = append(body, transEmpPacketEnd)
	body = append(body, U32(transEmpVersionCode)...)
	body = append(body, make([]byte, 14)...)
	// The record names the platform id again before its blocks (not the per-build AppID): a
	// 7.0.2 client's own fetch carries SubAppID here.
	body = append(body, U32(in.SubAppID)...)
	body = append(body, in.TLVs...)
	return append(body, transEmpPacketEnd)
}

// buildTransEmpPacket wraps the record in the layer the flow enciphers.
func buildTransEmpPacket(in TransEmpBody) []byte {
	body := buildTransEmpBody(in)
	out := make([]byte, 0, len(body)+16)
	out = append(out, transEmpKeyUsage)
	out = append(out, U16(uint16(len(body)))...)
	out = append(out, U32(in.SubAppID)...)
	out = append(out, U32(transEmpRole)...)
	out = append(out, U16(0)...) // uSt, empty
	out = append(out, 0)         // rollback, empty
	return append(out, body...)
}

// BuildTransEmp builds the plaintext trans_emp record that goes inside the OICQ frame. It does
// NOT encipher it: the OICQ layer (BuildOICQ) is what enciphers the body under the agreed key,
// so enciphering here as well would wrap the record in the key twice and the service, which
// deciphers once, would be left holding ciphertext where it expected the record — and would
// drop the request. The record travels enciphered exactly once, which is what a real client's
// own fetch does.
func BuildTransEmp(in TransEmpBody) []byte {
	plain := buildTransEmpPacket(in)
	if path := os.Getenv("OPENOPQ_DUMP_TE"); path != "" {
		if f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
			fmt.Fprintf(f, "%x\n", plain)
			_ = f.Close()
		}
	}
	return plain
}

// TransEmpReply is a reply once its body has been deciphered.
type TransEmpReply struct {
	// Command is the sub-command the reply answers.
	Command uint32
	AppID   uint32
	// Payload is the rest, which a sub-command reads to its own shape.
	Payload []byte
}

// UnwrapTransEmpReply reads the layer around a reply's record.
func UnwrapTransEmpReply(plain []byte) (TransEmpReply, error) {
	reader := NewReader(plain)
	if _, err := reader.U32(); err != nil {
		return TransEmpReply{}, fmt.Errorf("reply length: %w", err)
	}
	if _, err := reader.Take(4); err != nil {
		return TransEmpReply{}, fmt.Errorf("reply header: %w", err)
	}
	command, err := reader.U16()
	if err != nil {
		return TransEmpReply{}, fmt.Errorf("reply command: %w", err)
	}
	if _, err := reader.Take(40); err != nil {
		return TransEmpReply{}, fmt.Errorf("reply header: %w", err)
	}
	appID, err := reader.U32()
	if err != nil {
		return TransEmpReply{}, fmt.Errorf("reply application id: %w", err)
	}
	payload, err := reader.Take(reader.Remaining())
	if err != nil {
		return TransEmpReply{}, fmt.Errorf("reply payload: %w", err)
	}
	return TransEmpReply{Command: uint32(command), AppID: appID, Payload: payload}, nil
}

// QRFetchReply is what a fetch answers with.
type QRFetchReply struct {
	// Signature identifies the code for the polls that follow it.
	Signature []byte
	// URL is what the code encodes.
	URL string
	// Image is the code as a PNG.
	Image []byte
	// Blocks holds every TLV the reply carried, for callers that want the rest.
	Blocks map[uint16][]byte
}

// ParseQRFetchReply reads the payload of a fetch's answer.
//
// The block that carries the code is a protobuf whose field numbers have not been read out
// of the published implementations, so the URL is taken by its shape rather than by a field
// number: it is the one string in the block that begins a URL.
func ParseQRFetchReply(payload []byte) (QRFetchReply, error) {
	reader := NewReader(payload)
	if _, err := reader.U8(); err != nil {
		return QRFetchReply{}, fmt.Errorf("reply marker: %w", err)
	}
	signature, err := reader.LV16()
	if err != nil {
		return QRFetchReply{}, fmt.Errorf("reply signature: %w", err)
	}
	blocks, err := ReadTLVs(payload[reader.Pos():])
	if err != nil {
		return QRFetchReply{}, fmt.Errorf("reply blocks: %w", err)
	}
	reply := QRFetchReply{Signature: copyBytes(signature), Blocks: blocks}
	if block, ok := blocks[QRCodeTag]; ok {
		reply.URL = firstURL(block)
	}
	if image, ok := blocks[QRReplyImageTag]; ok {
		reply.Image = copyBytes(image)
	}
	if reply.URL == "" {
		return QRFetchReply{}, fmt.Errorf("the reply carries no code")
	}
	return reply, nil
}

// firstURL returns the first URL-shaped run in a block, or nothing.
func firstURL(block []byte) string {
	const scheme = "https://"
	start := bytes.Index(block, []byte(scheme))
	if start < 0 {
		return ""
	}
	end := start
	for end < len(block) && block[end] > 32 && block[end] < 127 {
		end++
	}
	return string(block[start:end])
}

// The blocks a confirmation carries. They are the scan's yield: the device key the login
// that follows is enciphered with, and the tickets it needs.
const (
	// QRConfirmTicketTag is the enciphered A1 the login hands back.
	QRConfirmTicketTag uint16 = 0x18
	// QRConfirmNoPicSigTag travels with the confirmation.
	QRConfirmNoPicSigTag uint16 = 0x19
	// QRConfirmDeviceKeyTag is the device key the following login is enciphered with.
	QRConfirmDeviceKeyTag uint16 = 0x1E
)

// QRDevice is what a fetch reports about the client it is sent from. Every field travels in
// the clear inside the request's blocks, so they have to describe the build the signer
// speaks for.
type QRDevice struct {
	AppID      uint32
	SubAppID   uint32
	MainSigMap uint32
	SSOVersion uint32
	// PTOSVersion is the platform/OS version number the fetch reports in TLVs 0x35 and 0x66.
	// It is the client's pt_os_version, not the SSO version: a macOS NT client sends 23 (Linux
	// sends 19). A capture-matched reimplementation writes this value, not SSOVersion.
	PTOSVersion uint32
	// PackageName and PTVersion are what the application block reports.
	PackageName string
	PTVersion   string
	// OS and DeviceName go into the block that describes the machine.
	OS         string
	DeviceName string
	Guid       []byte
}

// qrTypeBuf is the constant the code block carries.
var qrTypeBuf = []byte{0x30, 0x01}

// BuildQRFetchTLVs encodes the blocks a fetch carries, in the order the published
// implementation writes them.
//
// Every block here is a transcription of that source, field for field, with one exception:
// the last block is a protobuf whose field numbers were not in the file that named it, so it
// is built from the shape of its constructor — a system block first, then the type bytes.
// That is the one value in this file that is inferred rather than read, and a fetch the
// service accepts is what would confirm it.
func BuildQRFetchTLVs(device QRDevice) []byte {
	packageName := device.PackageName
	if packageName == "" {
		packageName = qrPackageName
	}
	// Block 0x16 is read field for field off a real 7.0.2 client's own fetch, deciphered with
	// that client's extracted ephemeral key: a zero, the platform id and then the build id (in
	// that order — the platform's half first), the guid, the package name, a version string that
	// is "2.0.0" here rather than the client's own version, and the package name a second time.
	block16 := make([]byte, 0, 80+len(device.Guid))
	block16 = append(block16, U32(0)...)
	block16 = append(block16, U32(device.SubAppID)...)
	block16 = append(block16, U32(device.AppID)...)
	block16 = append(block16, device.Guid...)
	block16 = append(block16, LV16([]byte(packageName))...)
	block16 = append(block16, LV16([]byte(qrBlock16Version))...)
	block16 = append(block16, LV16([]byte(packageName))...)

	block1b := make([]byte, 0, 32)
	for _, value := range []uint32{0, 0, 3, 4, 72, 2, 2} {
		block1b = append(block1b, U32(value)...)
	}
	block1b = append(block1b, U16(0)...)

	// Block 0x1d off the same captured fetch: a zero marker, a bitmap (the macOS client sends
	// 0x7ffc here, not the main signature map), a zero, and a zero byte.
	block1d := make([]byte, 0, 12)
	block1d = append(block1d, 0)
	block1d = append(block1d, U32(qrFetchBitmap)...)
	block1d = append(block1d, U32(0)...)
	block1d = append(block1d, 0)

	code := Marshal(
		BytesField(1, Marshal(
			StringField(1, device.OS),
			StringField(2, device.DeviceName),
		)),
		BytesField(4, qrTypeBuf),
	)

	// The payload opens with three fixed fields before its blocks: a u16 zero, a u64 zero,
	// and a u8 zero. This is exactly what the working reference (LagrangeGo's trans_emp fetch)
	// writes — no application id and no trailing u16 here. An earlier version of this file
	// inserted a u32 application id and a second u16 zero, which shifted the whole block
	// layout by six bytes so the service read the blocks at the wrong offset and dropped the
	// request without answering; those two extra fields are gone now.
	blocks := make([]byte, 0, 32)
	blocks = append(blocks, U16(0)...)
	blocks = append(blocks, make([]byte, 8)...)
	blocks = append(blocks, 0)
	blocks = append(blocks, WriteTLVs([]TLVItem{
		{Tag: QRFetchTLVSession, Value: block16},
		{Tag: 0x1B, Value: block1b},
		{Tag: QRFetchTLVDevice, Value: block1d},
		{Tag: 0x33, Value: copyBytes(device.Guid)},
		{Tag: 0x35, Value: U32(device.PTOSVersion)},
		{Tag: 0x66, Value: U32(device.PTOSVersion)},
		{Tag: QRCodeTag, Value: code},
	})...)
	return blocks
}

// BuildQRPoll encodes a poll's payload: the signature the fetch returned, and nothing else.
// qrPackageName is the application the flow speaks for.
const qrPackageName = "com.tencent.qq"

// BuildQRPoll encodes a poll's payload: the signature the fetch returned, and the three empty
// fields a published implementation puts after it. The payload opens with the signature's own
// length rather than with a block of its own, which is what tells a poll from a fetch.
func BuildQRPoll(signature []byte) []byte {
	out := make([]byte, 0, 24+len(signature))
	out = append(out, LV16(signature)...)
	out = append(out, make([]byte, 8)...)
	out = append(out, U32(0)...)
	out = append(out, 0)
	return out
}

// QRScanState is what a poll answers with.
//
// A poll that has nothing to report answers with one byte. A confirmation carries the
// account and the blocks below behind it — which is why the record is read whenever the
// payload holds more than that byte, rather than keyed on a state value: the byte's values
// are not established here, and a reply that carries extra bytes could only be a
// confirmation.
type QRScanState struct {
	// State is the byte the reply opened with, reported as it arrived.
	State byte
	// Confirmed is whether the record behind that byte was read.
	Confirmed bool
	// Uin is the account that scanned, when there is one.
	Uin uint32
	// Tgtgt is the device key the login that follows is enciphered with.
	Tgtgt []byte
	// Ticket is the enciphered credential the login hands back.
	Ticket []byte
	// NoPicSig travels with the confirmation.
	NoPicSig []byte
	// Blocks holds every block the confirmation carried.
	Blocks map[uint16][]byte
}

// ParseQRScanState reads a poll's answer.
//
// A reply that will not parse as a record is reported as a bare state rather than as a
// failure. That is what the flow itself does — it reads the record only for a confirmation
// and ignores everything else — and it is the safer default here, because the state byte's
// values are not established: a reply that said nothing would otherwise have to be guessed
// at, and a truncated confirmation lapses rather than being taken for an approval.
func ParseQRScanState(payload []byte) (QRScanState, error) {
	reader := NewReader(payload)
	state, err := reader.U8()
	if err != nil {
		return QRScanState{}, fmt.Errorf("scan state: %w", err)
	}
	out := QRScanState{State: state}
	rest := payload[reader.Pos():]
	if len(rest) == 0 {
		return out, nil
	}

	body := NewReader(rest)
	if _, err := body.U32(); err != nil {
		return out, nil
	}
	uin, err := body.U32()
	if err != nil {
		return out, nil
	}
	if _, err := body.U32(); err != nil {
		return out, nil
	}
	blocks, err := ReadTLVs(rest[body.Pos():])
	if err != nil {
		return out, nil
	}
	out.Confirmed = true
	out.Uin = uin
	out.Blocks = blocks
	if value, ok := blocks[QRConfirmDeviceKeyTag]; ok {
		out.Tgtgt = copyBytes(value)
	}
	if value, ok := blocks[QRConfirmTicketTag]; ok {
		out.Ticket = copyBytes(value)
	}
	if value, ok := blocks[QRConfirmNoPicSigTag]; ok {
		out.NoPicSig = copyBytes(value)
	}
	return out, nil
}
