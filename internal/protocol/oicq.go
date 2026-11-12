package protocol

import (
	"crypto/rand"
	"errors"
	"fmt"
)

// The OICQ frame wraps the body of a login request. It repeats the account and the version
// in a plaintext header, so the layer above can act on the packet before the body is
// deciphered, and it names the client's public key alongside the body so the server can
// answer in kind.
//
// The values are protocol identity, not tunables: the server checks the request against
// the build these describe. The names are the ones the published NTQQ implementations use,
// which is what fixed several of them: what reads as a three-byte marker is an extension
// version and a command version followed by the first byte of a zero, and what reads as a
// pair of 16-bit numbers is a publisher id, an instance id and the client version.
const (
	// oicqFrameOpen and oicqFrameClose bracket the frame.
	oicqFrameOpen  = 0x02
	oicqFrameClose = 0x03
	// oicqBodyMarker introduces each of the frame's two inner runs, and oicqKeyMarker
	// separates the nonce from the public key. Written as two bytes and a 16-bit value,
	// which is how the published implementations write them too.
	oicqBodyMarker      = 0x01
	oicqKeyMarker       = 0x0102
	oicqHeaderSize      = 25
	oicqReplyHeaderSize = 13
	// oicqNonceSize is the length of the random field in the first run.
	oicqNonceSize = 16

	oicqClientVersion uint16 = 8001
	// oicqExtensionVersion and oicqCommandVersion travel as single bytes after the account.
	oicqExtensionVersion = 3
	oicqCommandVersion   = 0x87
	// oicqInstanceID is the instance identifier, always zero in every capture.
	oicqInstanceID uint16 = 0
	// oicqAppClientVersion is the client's build version.
	oicqAppClientVersion uint16 = 13172
	// oicqDefaultPublisherID is the publisher id for the 7.0.x builds that were captured.
	// The published implementations write 19 where the capture has 5, so it is a build
	// constant rather than a protocol one, and it is an input here for the same reason.
	oicqDefaultPublisherID = 5
	// oicqDefaultCommand is the command the frame is for, not a version. The number is a
	// property of the service the packet is addressed to, which is why a login's frame and a
	// code's differ by two and nothing else about them does.
	oicqDefaultCommand uint16 = OICQTransEmpCommand
)

// The command numbers a frame carries, one per service. The published implementations name
// the login's as 2064 and the code fetch's as 2066, and both a capture and the client's own
// log agree — which is also why the reference codec's 2064 is right for the command it
// builds and wrong for the one it does not.
const (
	OICQLoginCommand    uint16 = 2064
	OICQTransEmpCommand uint16 = 2066
)

// ErrOICQFrame reports a packet that is not a well-formed OICQ frame.
var ErrOICQFrame = errors.New("protocol: malformed OICQ frame")

// OICQInput is what an OICQ frame is built from.
type OICQInput struct {
	Uin uint32
	// PublicKey is the client's elliptic-curve point, in the encoding the server expects
	// for the curve the reference calls P192: a 25-byte compressed point.
	PublicKey []byte
	// ShareKey enciphers the body.
	ShareKey []byte
	// Nonce fills the random field. An empty one is drawn from the system; supplying one
	// is what makes a frame reproducible for comparison.
	Nonce []byte
	// Command is the second header field. Zero means the captured default.
	Command uint16
	// PublisherID is a build constant. Zero means the captured default.
	PublisherID byte
}

func (in OICQInput) command() uint16 {
	if in.Command == 0 {
		return oicqDefaultCommand
	}
	return in.Command
}

func (in OICQInput) publisherID() byte {
	if in.PublisherID == 0 {
		return oicqDefaultPublisherID
	}
	return in.PublisherID
}

// BuildOICQ wraps a body in the OICQ frame and enciphers it with the share key.
//
// The result is the frame itself: it opens with the marker, carries a 16-bit length that
// counts the whole of it, and ends with the close marker. It does not carry the 32-bit length
// the service layer adds — that is the service layer's, and a frame that carried both would
// reach the service with four bytes more than it sent, which is also four bytes more than the
// signer was given. A capture settles which does which: the bytes handed to the signer are the
// frame, and the bytes on the wire are the frame behind one length.
func BuildOICQ(input OICQInput, body []byte) ([]byte, error) {
	if len(input.ShareKey) != teaKeySize {
		return nil, fmt.Errorf("%w: share key is %d bytes, want %d",
			ErrOICQFrame, len(input.ShareKey), teaKeySize)
	}
	nonce := input.Nonce
	if len(nonce) == 0 {
		nonce = make([]byte, oicqNonceSize)
		if _, err := rand.Read(nonce); err != nil {
			return nil, fmt.Errorf("generate OICQ nonce: %w", err)
		}
	}
	if len(nonce) != oicqNonceSize {
		return nil, fmt.Errorf("%w: nonce is %d bytes, want %d",
			ErrOICQFrame, len(nonce), oicqNonceSize)
	}
	enciphered, err := Encrypt(body, input.ShareKey)
	if err != nil {
		return nil, fmt.Errorf("encipher OICQ body: %w", err)
	}

	inner := make([]byte, 0, len(enciphered)+len(input.PublicKey)+oicqNonceSize+8)
	inner = append(inner, oicqBodyMarker, oicqBodyMarker)
	inner = append(inner, nonce...)
	inner = append(inner, U16(oicqKeyMarker)...)
	inner = append(inner, LV16(input.PublicKey)...)
	inner = append(inner, enciphered...)

	header := oicqHeader(input)
	// The length counts the two frame bytes as well as everything between them, so it
	// equals the size of the finished frame.
	framed := make([]byte, 0, len(header)+len(inner)+3)
	framed = append(framed, oicqFrameOpen)
	framed = append(framed, U16(uint16(len(header)+len(inner)+4))...)
	framed = append(framed, header...)
	framed = append(framed, inner...)
	return append(framed, oicqFrameClose), nil
}

// oicqHeader encodes the plaintext header of a request.
//
// The layout is the one a captured request carries, read field by field against the
// published implementations, which name each one: client version, command, sequence,
// account, extension and command versions, a constant, publisher id, instance id, client
// version and retry count.
func oicqHeader(input OICQInput) []byte {
	header := make([]byte, 0, oicqHeaderSize)
	header = append(header, U16(oicqClientVersion)...)
	header = append(header, U16(input.command())...)
	header = append(header, U16(0)...) // sequence
	header = append(header, U32(input.Uin)...)
	header = append(header, oicqExtensionVersion, oicqCommandVersion)
	header = append(header, U32(0)...) // a constant the published code writes too
	header = append(header, byte(input.publisherID()))
	header = append(header, U16(oicqInstanceID)...)
	header = append(header, U16(oicqAppClientVersion)...)
	return append(header, U32(0)...) // retry count
}

// UnwrapOICQ takes a body out of a request frame and deciphers it.
//
// The input is the frame with its 32-bit length already removed. The nonce and the public
// key are stepped over rather than returned: neither is needed to decipher the body, and
// the account layer keeps its own copy of the key.
func UnwrapOICQ(framed, shareKey []byte) ([]byte, error) {
	reader := NewReader(framed)
	if err := expectFrameOpen(reader); err != nil {
		return nil, err
	}
	if _, err := reader.U16(); err != nil {
		return nil, err
	}
	if _, err := reader.Take(oicqHeaderSize); err != nil {
		return nil, err
	}
	if err := expectBytes(reader, []byte{oicqBodyMarker, oicqBodyMarker}); err != nil {
		return nil, err
	}
	if _, err := reader.Take(oicqNonceSize); err != nil {
		return nil, err
	}
	if err := expectBytes(reader, U16(oicqKeyMarker)); err != nil {
		return nil, err
	}
	if _, err := reader.LV16(); err != nil {
		return nil, err
	}
	return readEncipheredBody(reader, shareKey)
}

// UnwrapOICQReply takes a body out of a reply frame and deciphers it.
//
// A reply frame is not the shape of a request one. Its header is shorter — the version
// marker and the three numbers behind it are absent, and the account is followed by three
// bytes that are zero where a request carries the marker — and there is no nonce or public
// key, because the service is not asking the client to answer in kind. Only one reply frame
// has been captured, so this reads what that one has rather than asserting what any other
// would.
func UnwrapOICQReply(framed, shareKey []byte) ([]byte, error) {
	reader := NewReader(framed)
	if err := expectFrameOpen(reader); err != nil {
		return nil, err
	}
	if _, err := reader.U16(); err != nil {
		return nil, err
	}
	if _, err := reader.Take(oicqReplyHeaderSize); err != nil {
		return nil, err
	}
	return readEncipheredBody(reader, shareKey)
}

// readEncipheredBody reads what remains, checks the frame closes, and deciphers it.
func readEncipheredBody(reader *Reader, shareKey []byte) ([]byte, error) {
	rest, err := reader.Take(reader.Remaining())
	if err != nil {
		return nil, err
	}
	if len(rest) == 0 || rest[len(rest)-1] != oicqFrameClose {
		return nil, fmt.Errorf("%w: frame does not close", ErrOICQFrame)
	}
	return Decrypt(rest[:len(rest)-1], shareKey)
}

// expectFrameOpen checks the opening byte.
func expectFrameOpen(reader *Reader) error {
	open, err := reader.U8()
	if err != nil {
		return err
	}
	if open != oicqFrameOpen {
		return fmt.Errorf("%w: frame opens with %#02x, want %#02x",
			ErrOICQFrame, open, oicqFrameOpen)
	}
	return nil
}

// expectBytes reads the next bytes and fails if they are not the expected marker.
func expectBytes(reader *Reader, want []byte) error {
	got, err := reader.Take(len(want))
	if err != nil {
		return err
	}
	for i := range want {
		if got[i] != want[i] {
			return fmt.Errorf("%w: expected marker %x, found %x", ErrOICQFrame, want, got)
		}
	}
	return nil
}
