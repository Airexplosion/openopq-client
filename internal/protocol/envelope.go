package protocol

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"io"
)

// serviceType is the value the service preamble opens with on a request.
const serviceType uint32 = 12

// SSOResponse is the framing around a reply.
//
// The layout is the same for an accepted reply and a refused one; what differs is which
// fields carry anything. Three replies back it: a request the client made and the answer it
// got, both captured off the wire, and a refusal the bot recorded. All three agree, and one
// of them being an acceptance is what makes this worth trusting for the login flow.
//
//	sequence
//	result code
//	message        empty when the service carried the request out
//	command        the service the reply belongs to
//	an empty field
//	compression    0 is uncompressed, 1 is zlib
//	block          a small protobuf whose fields grow as the exchange progresses
//	timestamp
type SSOResponse struct {
	// Sequence echoes the number the request carried. A reply nothing asked for has no
	// request to echo, and the one recorded refusal carries 10000 there instead — which is
	// why this is reported as a number and not called a sequence unconditionally.
	Sequence uint32
	// Code is the result. Zero means the request was carried out; anything else is a
	// refusal, and Message usually says why.
	Code int32
	// Message is the wording the service attached, empty when it attached none.
	Message string
	// Command names the service the reply belongs to. It is what a caller matches a reply
	// against before handing the body to a decoder.
	Command string
	// Compression is the body encoding declared by the SSO header, before normalization.
	Compression uint32
	// Block is the run of fields between the two fixed ones and the timestamp. Its contents
	// are a protobuf whose meaning is not established, so it is carried through untouched.
	Block []byte
	// Timestamp is the last number in the header.
	Timestamp uint32
	// Body retains its inclusive SSO length prefix. A zlib body is decompressed and given
	// a new length prefix so command parsers receive the same framing as plain replies.
	Body []byte
}

// Accepted reports whether the service carried the request out.
func (r SSOResponse) Accepted() bool { return r.Code == 0 }

// DecodeResponse turns a frame payload into a reply.
//
// A reply captured off the wire sits behind two layers: a service preamble, and an outer
// cipher that is QQTEA under an all-zero key. Both are known byte for byte on the request
// side, and the preamble is verified against a built request. The reply side was long the
// gap — nothing but an already-unwrapped sample had ever been seen — and a capture of the
// login flow closed it: a reply on the wire carries both layers, exactly as a request does.
//
// The two are tried rather than distinguished because a payload that is not enciphered
// cannot be mistaken for one that is, and the plaintext form is still what an
// already-unwrapped reply looks like.
func DecodeResponse(packet []byte) (SSOResponse, error) {
	return DecodeResponseWithKey(packet, nil)
}

// DecodeResponseWithKey is DecodeResponse with the session key the post-login replies are
// enciphered under. Before a login there is no such key and nil is passed; after one, the
// transport holds it and passes it so that a reply enciphered with the D2 key (cipher flag 1)
// — which is every reply to a post-login command, register among them — can be deciphered and
// matched to its request. Without it such a reply cannot be read, its sequence cannot be
// recovered, and the request that is waiting for it times out as if the service never answered.
func DecodeResponseWithKey(packet, d2Key []byte) (SSOResponse, error) {
	// A reply on the wire opens with its own preamble, which is not the one a request opens
	// with: the type, then a byte that says how the body behind it is enciphered, then a zero,
	// then the account written as text. The cipher byte is what a request has no equivalent of,
	// and getting it wrong is why a reply would not decode — the old code read the request's
	// preamble here and so stripped the wrong number of bytes off every reply.
	if cipher, body, ok := stripReplyPreamble(packet); ok {
		payload := body
		// EncryptTypeEmptyKey (2) is QQTEA under the all-zero key, the pre-login state a code
		// fetch lives in. Plain (0) is left as it is. The D2 key (1) is the login's session key,
		// held by the transport once there is a login and passed in here.
		switch cipher {
		case ssoReplyPlain:
		case ssoReplyEmptyKey:
			plain, err := Decrypt(body, make([]byte, teaKeySize))
			if err != nil {
				return SSOResponse{}, fmt.Errorf("reply empty-key cipher: %w", err)
			}
			payload = plain
		case ssoReplyD2Key:
			if len(d2Key) != teaKeySize {
				return SSOResponse{}, fmt.Errorf("reply D2 cipher requires a %d-byte session key (got %d bytes)", teaKeySize, len(d2Key))
			}
			plain, err := Decrypt(body, d2Key)
			if err != nil {
				return SSOResponse{}, fmt.Errorf("reply D2 cipher: %w", err)
			}
			payload = plain
		default:
			return SSOResponse{}, fmt.Errorf("reply preamble has unsupported cipher flag %d", cipher)
		}
		// A recognized preamble selects this framing. Preserve errors from its decrypted
		// header/body rather than trying to interpret the original wire packet as a bare reply.
		return UnwrapSSOResponse(payload)
	}
	// A payload that carries no preamble — an already-unwrapped sample, or one enciphered under
	// the all-zero key with the preamble already off — is handled the way it always was.
	payload := packet
	if plain, err := Decrypt(packet, make([]byte, teaKeySize)); err == nil {
		payload = plain
	}
	return UnwrapSSOResponse(payload)
}

// The cipher a reply's preamble names for its body.
const (
	ssoReplyPlain    byte = 0
	ssoReplyD2Key    byte = 1
	ssoReplyEmptyKey byte = 2
)

// stripReplyPreamble reads the preamble a reply opens with and returns the cipher byte and the
// body behind it. It reports false for a packet that does not open with one, so a caller can
// fall back to treating the packet as a bare, already-unwrapped body.
//
// The shape is the service's, read off the login flow and matched against a published
// implementation: a u32 type (12), a u8 cipher flag, a zero byte, and the account as a
// length-prefixed string (which is "0" before there is an account).
func stripReplyPreamble(packet []byte) (cipher byte, body []byte, ok bool) {
	reader := NewReader(packet)
	if value, err := reader.U32(); err != nil || value != serviceType {
		return 0, nil, false
	}
	cipher, err := reader.U8()
	if err != nil {
		return 0, nil, false
	}
	if marker, err := reader.U8(); err != nil || marker != 0 {
		return 0, nil, false
	}
	if _, err := reader.LV32(); err != nil { // the account, as text
		return 0, nil, false
	}
	rest, err := reader.Take(reader.Remaining())
	if err != nil {
		return 0, nil, false
	}
	return cipher, rest, true
}

// UnwrapSSOResponse reads the header and body out of a reply whose outer layers are off.
func UnwrapSSOResponse(plain []byte) (SSOResponse, error) {
	reader := NewReader(plain)
	header, err := reader.LV32()
	if err != nil {
		return SSOResponse{}, fmt.Errorf("reply header: %w", err)
	}

	fields := NewReader(header)
	response := SSOResponse{}
	if response.Sequence, err = fields.U32(); err != nil {
		return SSOResponse{}, fmt.Errorf("reply header: %w", err)
	}
	code, err := fields.U32()
	if err != nil {
		return SSOResponse{}, fmt.Errorf("reply header: %w", err)
	}
	response.Code = int32(code)
	message, err := fields.LV32()
	if err != nil {
		return SSOResponse{}, fmt.Errorf("reply header message: %w", err)
	}
	response.Message = string(message)
	command, err := fields.LV32()
	if err != nil {
		return SSOResponse{}, fmt.Errorf("reply header command: %w", err)
	}
	response.Command = string(command)
	// The field after the session ID declares body compression. The same position and
	// flag 1=zlib are used by LagrangeGo's network/response.go readSSOFrame.
	if _, err := fields.LV32(); err != nil {
		return SSOResponse{}, fmt.Errorf("reply header: %w", err)
	}
	if response.Compression, err = fields.U32(); err != nil {
		return SSOResponse{}, fmt.Errorf("reply header compression: %w", err)
	}
	if response.Block, err = fields.LV32(); err != nil {
		return SSOResponse{}, fmt.Errorf("reply header block: %w", err)
	}
	if response.Timestamp, err = fields.U32(); err != nil {
		return SSOResponse{}, fmt.Errorf("reply header timestamp: %w", err)
	}
	response.Body, err = decodeSSOBody(plain[reader.Pos():], response.Compression)
	if err != nil {
		return SSOResponse{}, err
	}
	return response, nil
}

// Decompression must not turn a small network frame into an unbounded allocation.
const maxDecompressedSSOBodySize = MaxFrameSize

func decodeSSOBody(body []byte, compression uint32) ([]byte, error) {
	switch compression {
	case 0:
		return copyBytes(body), nil
	case 8:
		// LagrangeGo's readSSOFrame removes the normal inclusive body length,
		// then skips another four bytes for flag 8. Keep our framed Body contract.
		reader := NewReader(body)
		wrapped, err := reader.LV32()
		if err != nil || reader.Remaining() != 0 {
			return nil, fmt.Errorf("reply flag-8 body: expected one complete inclusive-length block (bytes=%d)", len(body))
		}
		if len(wrapped) <= 4 {
			return LV32(nil), nil
		}
		return LV32(wrapped[4:]), nil
	case 1:
		reader := NewReader(body)
		compressed, err := reader.LV32()
		if err != nil || reader.Remaining() != 0 {
			return nil, fmt.Errorf("reply zlib body: expected one complete inclusive-length block (bytes=%d)", len(body))
		}
		source := bytes.NewReader(compressed)
		inflater, err := zlib.NewReader(source)
		if err != nil {
			return nil, fmt.Errorf("reply zlib body: %w", err)
		}
		defer inflater.Close()
		plain, err := io.ReadAll(io.LimitReader(inflater, maxDecompressedSSOBodySize+1))
		if err != nil {
			return nil, fmt.Errorf("reply zlib body: %w", err)
		}
		if len(plain) > maxDecompressedSSOBodySize {
			return nil, fmt.Errorf("reply zlib body exceeds %d decompressed bytes", maxDecompressedSSOBodySize)
		}
		if source.Len() != 0 {
			return nil, fmt.Errorf("reply zlib body has %d trailing bytes", source.Len())
		}
		return LV32(plain), nil
	default:
		return nil, fmt.Errorf("reply body has unsupported compression flag %d (bytes=%d)", compression, len(body))
	}
}

// stripServicePreamble removes the preamble a request opens with, if this packet has one.
// A packet that does not match is returned unchanged rather than rejected, because a reply
// is not obliged to carry one.
func stripServicePreamble(packet []byte) []byte {
	reader := NewReader(packet)
	if value, err := reader.U32(); err != nil || value != serviceType {
		return packet
	}
	if _, err := reader.U8(); err != nil {
		return packet
	}
	if _, err := reader.LV32(); err != nil { // the empty token field
		return packet
	}
	if _, err := reader.U8(); err != nil { // the marker before the account
		return packet
	}
	if _, err := reader.LV32(); err != nil { // the account, as text
		return packet
	}
	rest, err := reader.Take(reader.Remaining())
	if err != nil {
		return packet
	}
	return rest
}
