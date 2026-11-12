package protocol

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// ErrShortPacket reports a packet that ended before a field did. Callers treat it as a
// malformed packet, never as a value.
var ErrShortPacket = errors.New("protocol: packet ended early")

// Reader walks a packet. Every read is bounds checked, because these bytes come from
// the network and a short packet must produce an error rather than a panic.
type Reader struct {
	data []byte
	pos  int
}

// NewReader starts a reader at the beginning of a packet.
func NewReader(data []byte) *Reader { return &Reader{data: data} }

// Pos reports how far the reader has advanced.
func (r *Reader) Pos() int { return r.pos }

// Remaining reports the bytes not yet read.
func (r *Reader) Remaining() int { return len(r.data) - r.pos }

// Take reads exactly n bytes.
func (r *Reader) Take(n int) ([]byte, error) {
	if n < 0 || r.pos+n > len(r.data) {
		return nil, fmt.Errorf("%w: wanted %d bytes at offset %d of %d",
			ErrShortPacket, n, r.pos, len(r.data))
	}
	out := r.data[r.pos : r.pos+n]
	r.pos += n
	return out, nil
}

// U8 reads one byte.
func (r *Reader) U8() (byte, error) {
	value, err := r.Take(1)
	if err != nil {
		return 0, err
	}
	return value[0], nil
}

// U16 reads a big-endian 16-bit integer.
func (r *Reader) U16() (uint16, error) {
	value, err := r.Take(2)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint16(value), nil
}

// U32 reads a big-endian 32-bit integer.
func (r *Reader) U32() (uint32, error) {
	value, err := r.Take(4)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(value), nil
}

// LV16 reads a 16-bit length followed by that many bytes.
func (r *Reader) LV16() ([]byte, error) {
	size, err := r.U16()
	if err != nil {
		return nil, err
	}
	return r.Take(int(size))
}

// LV32 reads a 32-bit length followed by its payload. The stored length counts itself,
// so the payload is four bytes shorter than the number on the wire.
func (r *Reader) LV32() ([]byte, error) {
	size, err := r.U32()
	if err != nil {
		return nil, err
	}
	if size < 4 {
		return nil, fmt.Errorf("%w: LV32 length %d is shorter than its own prefix", ErrShortPacket, size)
	}
	return r.Take(int(size) - 4)
}

// U16 encodes a big-endian 16-bit integer.
func U16(value uint16) []byte {
	out := make([]byte, 2)
	binary.BigEndian.PutUint16(out, value)
	return out
}

// U32 encodes a big-endian 32-bit integer.
func U32(value uint32) []byte {
	out := make([]byte, 4)
	binary.BigEndian.PutUint32(out, value)
	return out
}

// LV16 prefixes a payload with its 16-bit length.
func LV16(payload []byte) []byte {
	out := make([]byte, 0, 2+len(payload))
	out = append(out, U16(uint16(len(payload)))...)
	return append(out, payload...)
}

// LV32 prefixes a payload with a 32-bit length that includes the prefix itself.
func LV32(payload []byte) []byte {
	out := make([]byte, 0, 4+len(payload))
	out = append(out, U32(uint32(len(payload)+4))...)
	return append(out, payload...)
}

// TLV encodes one tag-length-value item.
func TLV(tag uint16, payload []byte) []byte {
	out := make([]byte, 0, 4+len(payload))
	out = append(out, U16(tag)...)
	return append(out, LV16(payload)...)
}

// ReadTLVs parses a counted list of TLV items.
//
// The protocol allows a tag to appear more than once and leaves the meaning to the
// reader; the reference behaviour is that the last occurrence wins, which is what this
// returns. Concatenating instead would silently merge two different fields.
func ReadTLVs(data []byte) (map[uint16][]byte, error) {
	reader := NewReader(data)
	count, err := reader.U16()
	if err != nil {
		return nil, err
	}
	items := make(map[uint16][]byte, count)
	for i := 0; i < int(count); i++ {
		tag, err := reader.U16()
		if err != nil {
			return nil, err
		}
		value, err := reader.LV16()
		if err != nil {
			return nil, err
		}
		items[tag] = value
	}
	return items, nil
}

// WriteTLVs encodes a counted list of TLV items in the given order.
func WriteTLVs(items []TLVItem) []byte {
	out := make([]byte, 0, 2+len(items)*8)
	out = append(out, U16(uint16(len(items)))...)
	for _, item := range items {
		out = append(out, TLV(item.Tag, item.Value)...)
	}
	return out
}

// TLVItem is one tag/value pair in write order.
type TLVItem struct {
	Tag   uint16
	Value []byte
}
