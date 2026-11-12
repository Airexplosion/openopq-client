package protocol

import (
	"encoding/binary"
	"fmt"
	"io"
)

// MaxFrameSize bounds one frame. The length arrives before the body does, so without a
// bound a broken or hostile peer could ask for an arbitrarily large allocation before
// sending anything. The login packets are a few kilobytes at most.
const MaxFrameSize = 1 << 22

// ReadFrame reads one length-prefixed packet from a stream.
//
// The stored length counts itself, so a frame of n bytes is preceded by n. The returned
// slice is the payload with the length removed.
func ReadFrame(r io.Reader) ([]byte, error) {
	var prefix [4]byte
	if _, err := io.ReadFull(r, prefix[:]); err != nil {
		return nil, fmt.Errorf("read frame length: %w", err)
	}
	size := binary.BigEndian.Uint32(prefix[:])
	if size < 4 {
		return nil, fmt.Errorf("%w: frame length %d is shorter than its own prefix",
			ErrShortPacket, size)
	}
	if size > MaxFrameSize {
		// A length this far out usually means the packet is not framed the way this expects,
		// and what is there instead is the only thing that says what it is. Nothing can be
		// done with the stream after this, so reading a little more of it costs nothing.
		preview := make([]byte, 64)
		read, _ := io.ReadFull(r, preview)
		return nil, fmt.Errorf("%w: frame length %d exceeds the %d byte limit; it begins %x",
			ErrShortPacket, size, MaxFrameSize, append(prefix[:], preview[:read]...))
	}
	payload := make([]byte, size-4)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, fmt.Errorf("read frame body: %w", err)
	}
	return payload, nil
}

// WriteFrame writes a payload with the length prefix the stream expects.
func WriteFrame(w io.Writer, payload []byte) error {
	if _, err := w.Write(LV32(payload)); err != nil {
		return fmt.Errorf("write frame: %w", err)
	}
	return nil
}
