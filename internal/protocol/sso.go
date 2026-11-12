package protocol

import (
	"encoding/hex"
	"os"
	"strconv"
	"strings"
)

// The SSO layer wraps a command body in a header the server authenticates, then
// enciphers the whole thing.
//
// There are two states and the layer says which one it is in. Before a login there is no
// session key, and the only thing the cipher does is obfuscate the outer frame — which is the
// state every capture of this layer was taken in, because a client fetches a code and polls
// for it before it has an account. After a login the session key protects the payload, and
// two more fields fill in: the ticket the service layer carries, and the byte that says which
// cipher is in use. Getting the second state wrong is not a degraded connection, it is a
// service that refuses everything, so it is worth being exact about.
var ssoFrameKey = make([]byte, teaKeySize)

// SSOExtensionType accompanies every request and identifies the sign bundle's flavour.
const SSOExtensionType = 101

// Framing names which of the two shapes the service layer takes.
//
// There are two, and they are not interchangeable by accident: the older one carries the
// account as text after a marker, and the newer one carries four zero bytes and a byte where
// the older one has its marker. Which a build sends is a property of the build's generation.
type Framing int

const (
	// FramingClassic is the shape every capture of the 6.9.x era shows, and the one the
	// implementation that was verified against a real login sends.
	FramingClassic Framing = iota
	// FramingSeven is the shape a 7.0.2 client was captured sending. What its two fields mean
	// is not established, so they are written as the capture has them.
	FramingSeven
)

// SSORequest is everything needed to build one service request.
type SSORequest struct {
	Sequence uint32
	AppID    uint32
	MsfID    uint32
	// Framing is which of the two service layers to write. The zero value is the classic one.
	Framing Framing
	// Token is the ticket a request is authenticated by, which the login yields as its 0x10A
	// block. Empty before login, and empty is what a capture of the pre-login layer shows.
	Token []byte
	// Device is the device identifier the header reports. A published implementation puts the
	// same sixteen-byte GUID here that every other block reports, so a client whose two
	// disagree is claiming to be two machines.
	Device string
	Uin    uint32
	// Command is the service name, for example wtlogin.login.
	Command string
	// UID is the account's modern identifier, carried in extension field 16 once a login has
	// produced it. Empty before login.
	UID string
	// NTSign, Signature, Extra and DeviceToken are what the extension carries. A nil Signature
	// leaves field 24 out entirely, which is different from sending it empty; the other two
	// are left out of it when they are empty.
	NTSign      string
	Signature   []byte
	Extra       []byte
	DeviceToken []byte
	// D2 and SessionKey are the login's: the ticket the service layer carries, and the key the
	// payload is enciphered under. Both are empty before a login, and the layer falls back to
	// what a capture of that state shows.
	D2         []byte
	SessionKey []byte
	// Timestamp is seconds since the epoch; the caller supplies it so a packet can be
	// reproduced exactly.
	Timestamp int64
	// Body is the command payload. For wtlogin.login it is already framed; every other
	// command gets a length prefix added here.
	Body []byte
}

// frameKey is what the payload is enciphered under. Without a session key the layer only
// obfuscates, which is a state a capture shows; with one, the session key protects it.
func (r SSORequest) frameKey() []byte {
	if len(r.SessionKey) == 0 {
		return ssoFrameKey
	}
	return r.SessionKey
}

// cipherMarker names which of the two a packet is. The service layer carries it as a byte, and
// it is the field a client that had logged in but kept sending the pre-login value would be
// refused on.
func (r SSORequest) cipherMarker() byte {
	if len(r.D2) == 0 {
		return 2
	}
	return 1
}

// BuildSSO encodes the request into the outer frame the transport carries.
func BuildSSO(request SSORequest) ([]byte, error) {
	extension := buildExtension(request)
	header := buildHeader(request, extension)

	// Every command's body is wrapped here, whatever it is: an OICQ frame arrives bare and so
	// does an OIDB envelope, and the service reads both behind one length. A body that arrived
	// already wrapped would reach the service with four bytes it did not send, and the signer
	// would have been given those four bytes too.
	inner := append(LV32(header), LV32(request.Body)...)

	payload, err := Encrypt(inner, request.frameKey())
	if err != nil {
		return nil, err
	}

	service := make([]byte, 0, len(payload)+16)
	service = append(service, U32(12)...)
	service = append(service, request.cipherMarker())
	if request.Framing == FramingSeven {
		// The form a 7.0.x client was captured sending. It differs from the classic one in how
		// the account is written — four bytes rather than text — and in the byte that follows
		// it. Nothing says what that byte is, so it is carried as the capture has it.
		service = append(service, LV32(nil)...)
		service = append(service, U32(request.Uin)...)
		service = append(service, 0x04)
	} else {
		service = append(service, LV32(request.D2)...)
		service = append(service, 0)
		service = append(service, LV32([]byte(strconv.FormatUint(uint64(request.Uin), 10)))...)
	}
	service = append(service, payload...)
	// The result is the frame body, not a frame wrapped in its own length: the transport's
	// WriteFrame prepends the one stream length every packet carries, exactly as it does for the
	// keep-alive (see BuildHeartbeat, whose first four bytes are a service number, not a length).
	// Returning LV32(service) here instead would put a second length ahead of the first, and a
	// doubly-prefixed frame reaches the service as a length followed by bytes it cannot read as a
	// request — which it answers with silence. That was the bug that made trans_emp go unanswered
	// while a raw replay of the same bytes was accepted.
	return service, nil
}

// wtloginCommand is the one command whose body arrives already framed.
const wtloginCommand = "wtlogin.login"

// buildExtension encodes the sign bundle's container. Field 13 is a marker, 15 carries
// the client's own sign, 23 binds the request to a connection and moment, 24 is the
// packet signature and 26 names the sign flavour.
func buildExtension(request SSORequest) []byte {
	var fields []Field
	// A 7.0.x request's extension opens with the device GUID in field 12, written as the
	// thirty-two-character hex string — the same text the header's device field carries, not
	// the sixteen raw bytes. The classic layer had no field 12. A capture of a 7.0.x
	// wtlogin.trans_emp the server answered carries field 12 ahead of the marker and the sign,
	// and openopq omitted it, so the extension did not match what the service reads and the
	// request went unanswered.
	if request.Framing == FramingSeven && request.Device != "" {
		fields = append(fields, StringField(ssoExtensionGuidField, request.Device))
	}
	fields = append(fields,
		BytesField(ssoExtensionMarkerField, []byte{0}),
		StringField(ssoExtensionNTSignField, request.NTSign),
	)
	// Field 16 is the account's modern UID, and the authoritative NT reference (LagrangeGo
	// transport.go, head[16]=Sig.UID) carries it on every request. It is empty before a login
	// (no account yet, which is why OPQ's login-only codec never sends it), but once logged in
	// it is how the trpc status service identifies the session — and register is the first
	// trpc command after login. Omitting it is why register got -10003 ("re-login"): the
	// service could not bind the packet to the account. So send it whenever it is known.
	if request.UID != "" {
		fields = append(fields, StringField(ssoExtensionUIDField, request.UID))
	}
	fields = append(fields, BytesField(ssoExtensionMomentField, Marshal(
		StringField(1, "client_conn_seq"),
		StringField(2, strconv.FormatInt(request.Timestamp, 10)),
	)))
	if request.Signature != nil {
		fields = append(fields, BytesField(signBlockField, signBlock(request)))
	}
	fields = append(fields, VarintField(ssoExtensionFlavourField, SSOExtensionType))
	return Marshal(fields...)
}

// signBlock is what field 24 carries.
//
// It is not the signature on its own. Both a capture and an implementation that logs in with
// it settle the same three parts: the signature, the device token beside it, and the extra.
// They are written in that order, which is the order the implementation writes them in — a
// reader does not care, but a service that re-reads what it was sent might.
func signBlock(request SSORequest) []byte {
	parts := []Field{BytesField(signBlockSignature, request.Signature)}
	// A part that is not there is left out rather than sent empty: the two are different
	// packets, and only the first was ever observed.
	if len(request.DeviceToken) > 0 {
		parts = append(parts, BytesField(signBlockToken, request.DeviceToken))
	}
	if len(request.Extra) > 0 {
		parts = append(parts, BytesField(signBlockExtra, request.Extra))
	}
	return Marshal(parts...)
}

// The extension's numbers, all of them the capture's: a marker, the client's own sign, the
// connection's moment, the sign block, and the sign flavour.
const (
	// ssoExtensionGuidField carries the device GUID in a 7.0.x request's extension.
	ssoExtensionGuidField   = 12
	ssoExtensionMarkerField = 13
	ssoExtensionNTSignField = 15
	// ssoExtensionUIDField carries the account's modern UID (LagrangeGo head[16]).
	ssoExtensionUIDField = 16
	ssoExtensionMomentField = 23
	// signBlockField is the extension field the sign block travels in.
	signBlockField = 24
	// ssoExtensionFlavourField names the sign flavour.
	ssoExtensionFlavourField = 26
)

// The three parts of the sign block. The numbers are the capture's.
const (
	signBlockSignature = 1
	signBlockToken     = 2
	signBlockExtra     = 3
)

// The twelve bytes between the service numbers and the ticket. The two generations differ in
// them: the classic one opens with a two and the 7.0.x one opens with a zero and has a one
// ten bytes in. What any of it means is not established, so each is written the way its own
// capture has it rather than assembled from parts that would be named by guesswork.
var (
	classicPrefix = []byte{0x02, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	sevenPrefix   = []byte{0x00, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x01, 0x00}
)

func buildHeader(request SSORequest, extension []byte) []byte {
	prefix := classicPrefix
	if request.Framing == FramingSeven {
		prefix = sevenPrefix
	}
	header := make([]byte, 0, 64)
	header = append(header, U32(request.Sequence)...)
	header = append(header, U32(request.AppID)...)
	header = append(header, U32(request.MsfID)...)
	header = append(header, prefix...)
	header = append(header, LV32(request.Token)...)
	header = append(header, LV32([]byte(request.Command))...)
	header = append(header, LV32(nil)...)
	// The eighth field is the device GUID. The authoritative NT reference (LagrangeGo
	// transport.go PackPacket) writes the SIXTEEN RAW GUID BYTES here — io.MustParseHexStr(GUID)
	// — for every command, login and register alike. openopq used to write the THIRTY-TWO-char
	// hex STRING instead, which pre-login commands tolerate (login got code=0) but the
	// post-login register's device check does not, giving -10003. So decode to the raw bytes.
	// OPENOPQ_HEADER_DEVICE selects: "raw" (default, LagrangeGo), "hex" (old 32-char), "empty".
	switch strings.ToLower(strings.TrimSpace(os.Getenv("OPENOPQ_HEADER_DEVICE"))) {
	case "hex":
		header = append(header, LV32([]byte(request.Device))...)
	case "empty":
		header = append(header, LV32(nil)...)
	default:
		raw, err := hex.DecodeString(request.Device)
		if err != nil {
			raw = nil
		}
		header = append(header, LV32(raw)...)
	}
	header = append(header, LV32(nil)...)
	header = append(header, U16(2)...)
	return append(header, LV32(extension)...)
}
