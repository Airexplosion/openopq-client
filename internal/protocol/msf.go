package protocol

import (
	"bytes"
	"fmt"
)

// The hello every connection opens with.
//
// It is neither a framed packet nor a service request. It goes out as soon as the socket is
// up, before anything else, and the service answers with the same bytes and four more. What
// the fields are is not named by anything that could be reached — the middle of it reads as
// the text "MSF", which is the only part that says anything at all — so it is carried as the
// bytes a real client was seen to send rather than assembled from parts that would be guessed
// at.
//
// Skipping it does not fail. It is worse than that: the service accepts the connection, reads
// what is sent, and answers nothing, which is indistinguishable from a service that has
// nothing to say. That is what makes it worth a file of its own rather than a line somewhere.
var msfHello = []byte{
	0x00, 0x00, 0x00, 0x15, // the length, counting itself
	0x01,
	0x33, 0x52, 0x39, 0x00, 0x00, 0x00, 0x00, 0x04,
	'M', 'S', 'F',
	0x05, 0x00, 0x00, 0x00, 0x00,
}

// msfHelloSize is how long the greeting is, which the test below pins to the bytes above so
// the two cannot drift apart.
const msfHelloSize = 21

// msfAckExtra is one value the service was seen to add to the hello when it answers. It is
// four bytes the service assigns per connection, not a constant: a live connection was
// answered with 0x26AF683F where a capture had the value below, so the addition is read but
// not asserted. It stays here as a sample the tests build a well-formed answer from.
var msfAckExtra = []byte{0xDF, 0x58, 0x03, 0xF8}

// MSFHello returns the greeting a connection begins with.
func MSFHello() []byte { return copyBytes(msfHello) }

// MSFAckSize is how long the answer is, so a reader knows how much to read before it can tell
// whether the answer is the right one.
const MSFAckSize = msfHelloSize + 4

// CheckMSFAck confirms an answer is the one the service sends to the greeting. A connection
// whose greeting was not answered is one that will carry nothing, and saying so here is better
// than a caller waiting on a service that has already decided to ignore it.
//
// The answer is the greeting's own bytes, from after its length, with four more on the end.
// Only the echo identifies the answer as the service's; the four-byte addition is assigned by
// the service per connection — it varies from one connection to the next — so it is read to
// confirm the length but never asserted against a fixed value.
func CheckMSFAck(ack []byte) error {
	if len(ack) != MSFAckSize {
		return fmt.Errorf("protocol: the greeting was answered with %d bytes, want %d",
			len(ack), MSFAckSize)
	}
	// ack[4:] is the greeting's bytes from after its length, plus the per-connection addition.
	echoed := len(msfHello) - 4
	if !bytes.Equal(ack[4:4+echoed], msfHello[4:]) {
		return fmt.Errorf("protocol: the greeting was answered with something else: %x", ack)
	}
	return nil
}
