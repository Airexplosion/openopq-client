package protocol

import (
	"encoding/binary"
	"fmt"
)

// The protobuf wire types. The login packets use only varint and length-delimited; the
// rich-media (Highway) responses also carry fixed32/fixed64 (e.g. a server IP), so those are
// decoded too — as their little-endian integer value, stored like a varint field.
const (
	protobufWireVarint  = 0
	protobufWireFixed64 = 1
	protobufWireBytes   = 2
	protobufWireFixed32 = 5
)

// Field is one protobuf field. Fields are written in the order given, because these
// packets are compared byte-for-byte against captured ones and field order is part of
// the encoding.
type Field struct {
	Tag    int
	Number uint64
	Data   []byte
	IsData bool
}

// VarintField builds a numeric field.
func VarintField(tag int, value uint64) Field {
	return Field{Tag: tag, Number: value}
}

// BytesField builds a length-delimited field.
func BytesField(tag int, value []byte) Field {
	return Field{Tag: tag, Data: value, IsData: true}
}

// StringField builds a length-delimited field from text.
func StringField(tag int, value string) Field {
	return BytesField(tag, []byte(value))
}

// Varint encodes an unsigned integer in protobuf's base-128 form.
func Varint(value uint64) []byte {
	out := make([]byte, 0, 10)
	for value > 127 {
		out = append(out, byte(value&0x7F)|0x80)
		value >>= 7
	}
	return append(out, byte(value))
}

// Marshal encodes fields in order.
func Marshal(fields ...Field) []byte {
	out := make([]byte, 0, len(fields)*8)
	for _, field := range fields {
		if field.IsData {
			out = append(out, Varint(uint64(field.Tag<<3|protobufWireBytes))...)
			out = append(out, Varint(uint64(len(field.Data)))...)
			out = append(out, field.Data...)
			continue
		}
		out = append(out, Varint(uint64(field.Tag<<3|protobufWireVarint))...)
		out = append(out, Varint(field.Number)...)
	}
	return out
}

// Unmarshal decodes a protobuf message into its fields. Numeric fields become uint64 and
// length-delimited ones become []byte, which is what the login packets contain.
//
// A message that carries a list cannot be read this way — a map holds one value per tag, so
// every entry but the last would be gone — and EveryField is what those read through.
func Unmarshal(data []byte) (map[int]any, error) {
	fields, err := EveryField(data)
	if err != nil {
		return nil, err
	}
	out := make(map[int]any, len(fields))
	for _, field := range fields {
		if field.IsData {
			out[field.Tag] = field.Data
		} else {
			out[field.Tag] = field.Number
		}
	}
	return out, nil
}

// EveryField decodes a protobuf message into its fields in the order they were written,
// repeats included. A repeated field is how a message carries a list, and only this keeps
// all of them.
func EveryField(data []byte) ([]Field, error) {
	reader := NewReader(data)
	var out []Field
	for reader.Remaining() > 0 {
		key, err := ReadVarint(reader)
		if err != nil {
			return nil, err
		}
		tag, wire := int(key>>3), key&7
		if tag == 0 {
			return nil, fmt.Errorf("protocol: protobuf field tag 0")
		}
		value, err := readField(reader, wire)
		if err != nil {
			return nil, err
		}
		switch typed := value.(type) {
		case []byte:
			out = append(out, BytesField(tag, typed))
		case uint64:
			out = append(out, VarintField(tag, typed))
		}
	}
	return out, nil
}

// FieldsWith returns every field carrying one tag, which is how a repeated field is read.
func FieldsWith(fields []Field, tag int) []Field {
	var out []Field
	for _, field := range fields {
		if field.Tag == tag {
			out = append(out, field)
		}
	}
	return out
}

// FirstField returns the first field carrying one tag, which is how a field that is not
// repeated is read.
func FirstField(fields []Field, tag int) (Field, bool) {
	for _, field := range fields {
		if field.Tag == tag {
			return field, true
		}
	}
	return Field{}, false
}

func readField(reader *Reader, wire uint64) (any, error) {
	switch wire {
	case protobufWireVarint:
		return ReadVarint(reader)
	case protobufWireBytes:
		size, err := ReadVarint(reader)
		if err != nil {
			return nil, err
		}
		if size > uint64(reader.Remaining()) {
			return nil, ErrShortPacket
		}
		return reader.Take(int(size))
	case protobufWireFixed64:
		b, err := reader.Take(8)
		if err != nil {
			return nil, err
		}
		return binary.LittleEndian.Uint64(b), nil
	case protobufWireFixed32:
		b, err := reader.Take(4)
		if err != nil {
			return nil, err
		}
		return uint64(binary.LittleEndian.Uint32(b)), nil
	default:
		return nil, fmt.Errorf("protocol: unsupported protobuf wire type %d", wire)
	}
}

// ReadVarint decodes one base-128 integer.
func ReadVarint(reader *Reader) (uint64, error) {
	var value uint64
	for shift := 0; shift < 70; shift += 7 {
		b, err := reader.U8()
		if err != nil {
			return 0, err
		}
		value |= uint64(b&0x7F) << shift
		if b&0x80 == 0 {
			return value, nil
		}
	}
	return 0, fmt.Errorf("protocol: protobuf varint is too long")
}
