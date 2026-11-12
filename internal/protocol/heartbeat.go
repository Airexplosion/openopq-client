package protocol

import (
	"encoding/hex"
	"strconv"
)

// The keep-alive.
//
// A connection is held open by a packet the client sends every ten seconds or so, addressed
// to a service of its own. It is not a request in the usual sense: it carries no body to act
// on, and its answer says only that the connection is still there.
//
// What it is built from was read off a capture by comparing packets rather than by reading
// one. Ten of them, sent over one connection, differ in exactly one byte — the sequence —
// so every other byte is either a constant or something that belongs to the connection. Two
// further captures, taken from different connections and different servers, agree on the
// rest, which is what separates a value from a coincidence.
//
// Three things are therefore named and supplied rather than derived. The connection's
// identifier is two bytes the client picks; nothing observed says how. The install token is
// the same across captures, so it belongs to the installation rather than the connection,
// and the published sign contract is where a client gets one. And the run of bytes between
// the command and the token reads as an empty block and two more fields, which no capture
// contradicts but nothing names either — it is carried as it was found.
//
// This is the ten-second packet and not the only one there is. Published implementations
// describe two: this one, which keeps the link itself alive, and a second that reports at
// the protocol layer roughly every four and a half minutes. The second is not here, because
// no capture contains one and inventing a body to send on a timer would be guessing at a
// packet whose shape nothing observed. What is known is its cadence, which is not enough to
// build from.
const (
	// HeartbeatCommand is the name a keep-alive reports.
	HeartbeatCommand = "Heartbeat.Alive"
	// heartbeatService is the service number a keep-alive goes to. It is not the number a
	// request goes to, and its packets are not enciphered the way those are.
	heartbeatService uint32 = 13
	// heartbeatMarker and heartbeatField sit between the sequence and the command.
	heartbeatMarker byte   = 4
	heartbeatField  uint32 = 100
)

// heartbeatPrefix is the run between the command's block and the install token: an empty
// length-prefixed block and three more bytes. Carried verbatim.
var heartbeatPrefix = []byte{0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00}

// heartbeatTrailer is what follows the extension. It reads as two numbers and nothing else,
// and it is the same in every capture.
var heartbeatTrailer = []byte{0x00, 0x00, 0x00, 0x08, 0x00, 0x00, 0x00, 0x04}

// HeartbeatInput is what a keep-alive is built from.
type HeartbeatInput struct {
	// ConnectionID is the two bytes that belong to the connection.
	ConnectionID uint16
	// Sequence rises by one per packet, as the service's own does.
	Sequence byte
	// Token identifies the installation and is the same in every packet it sends.
	Token string
	// ConnectionTime is the moment the connection was made. The extension reports it, and it
	// is the same in every keep-alive on one connection.
	ConnectionTime int64
}

// CorrectTimeCommand is the second packet the status service carries. It goes out beside the
// keep-alive, before anything is asked for, and a capture shows it answered the same way.
const CorrectTimeCommand = "Client.CorrectTime"

// correctTimeField is the number in the packet that separates it from the keep-alive. The two
// are otherwise the same template.
const correctTimeField uint32 = 103

// BuildHeartbeat encodes a keep-alive.
//
// The result is the packet's own frame: a keep-alive is not wrapped again, which is why its
// first four bytes are the service number rather than a length.
func BuildHeartbeat(in HeartbeatInput) []byte {
	return buildStatus(in, HeartbeatCommand, heartbeatField)
}

// BuildCorrectTime encodes the packet that reports the client's time.
//
// It is the keep-alive's template with two values changed, which is what the two captures
// show: the same length, the same connection numbers, and a frame that differs only in the
// command and the number beside it.
func BuildCorrectTime(in HeartbeatInput) []byte {
	return buildStatus(in, CorrectTimeCommand, correctTimeField)
}

func buildStatus(in HeartbeatInput, command string, field uint32) []byte {
	out := make([]byte, 0, 128)
	out = append(out, U32(heartbeatService)...)
	out = append(out, U16(0)...)
	out = append(out, U16(in.ConnectionID)...)
	out = append(out, in.Sequence)
	out = append(out, U32(0)...)
	out = append(out, heartbeatMarker)
	out = append(out, U32(field)...)
	out = append(out, LV32([]byte(command))...)
	out = append(out, heartbeatPrefix...)
	out = append(out, []byte(in.Token)...)
	out = append(out, heartbeatExtension(in)...)
	return append(out, heartbeatTrailer...)
}

// heartbeatExtension is the block a keep-alive carries: the connection's moment, and the
// flavour marker. Unlike a request's extension it names no device and carries no signature —
// a keep-alive is not signed, because there is nothing in it to protect.
func heartbeatExtension(in HeartbeatInput) []byte {
	return Marshal(
		BytesField(23, Marshal(
			StringField(1, "client_conn_seq"),
			StringField(2, strconv.FormatInt(in.ConnectionTime, 10)),
		)),
		VarintField(26, SSOExtensionType),
	)
}

// HeartbeatToken renders a device token as the keep-alive reports it.
//
// Every capture carries "Ib " followed by thirty-two hex digits, which is what sixteen bytes
// look like written out, and the published sign contract hands out a device token of exactly
// that size. No capture pairs the two, so this is the reading that fits rather than a fact
// that was observed — a keep-alive the service accepts is what would settle it.
func HeartbeatToken(deviceToken []byte) string {
	return "Ib " + hex.EncodeToString(deviceToken)
}
