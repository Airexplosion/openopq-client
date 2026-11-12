// Package protocol implements the wire formats a QQ client speaks.
//
// Everything here is written from the packet layout itself — field order, byte order,
// block size — rather than from any existing implementation's source. The layouts are
// facts about the protocol; the code below is ours, and the tests check it against
// packets captured from a real client.
package protocol

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
)

// The cipher is TEA in Tencent's arrangement: 16 rounds over 64-bit blocks in
// big-endian words, chained so each block is mixed with both the previous plaintext and
// the previous ciphertext block.
const (
	teaRounds = 16
	// Typed, so the round sum wraps at 32 bits the way the cipher specifies instead of
	// being rejected as an overflowing constant.
	teaDelta     uint32 = 0x9E3779B9
	teaBlockSize        = 8
	teaKeySize          = 16
	// A QQTEA payload always ends with seven zero bytes and begins with a byte whose
	// low three bits give the padding length, so it can never be shorter than this.
	teaMinPlaintext = 16
)

// ErrCiphertext rejects input that cannot be a QQTEA payload.
var ErrCiphertext = errors.New("protocol: malformed QQTEA ciphertext")

// teaBlock encrypts or decrypts one 8-byte block.
func teaBlock(block []byte, key []byte, decrypt bool) []byte {
	a := binary.BigEndian.Uint32(block[0:4])
	b := binary.BigEndian.Uint32(block[4:8])
	k0 := binary.BigEndian.Uint32(key[0:4])
	k1 := binary.BigEndian.Uint32(key[4:8])
	k2 := binary.BigEndian.Uint32(key[8:12])
	k3 := binary.BigEndian.Uint32(key[12:16])

	// Sixteen rounds worth of the delta, wrapped at 32 bits. The unreduced product does
	// not fit in a uint32 constant, so the wrapped value is spelled out.
	const roundSum uint32 = 0xE3779B90

	var total uint32
	if decrypt {
		total = roundSum
	}
	for i := 0; i < teaRounds; i++ {
		if decrypt {
			b -= ((a << 4) + k2) ^ (a + total) ^ ((a >> 5) + k3)
			a -= ((b << 4) + k0) ^ (b + total) ^ ((b >> 5) + k1)
			total -= teaDelta
			continue
		}
		total += teaDelta
		a += ((b << 4) + k0) ^ (b + total) ^ ((b >> 5) + k1)
		b += ((a << 4) + k2) ^ (a + total) ^ ((a >> 5) + k3)
	}
	out := make([]byte, teaBlockSize)
	binary.BigEndian.PutUint32(out[0:4], a)
	binary.BigEndian.PutUint32(out[4:8], b)
	return out
}

// Encrypt pads a payload the way the protocol requires and enciphers it.
//
// The first padding byte carries the padding length in its low three bits, so it has to
// be random in its high bits; the rest is random. That randomness is why encryption
// output is checked by decryption rather than by comparison.
func Encrypt(plain, key []byte) ([]byte, error) {
	if len(key) != teaKeySize {
		return nil, fmt.Errorf("protocol: key must be %d bytes, got %d", teaKeySize, len(key))
	}
	padding := (-len(plain) - 10) % teaBlockSize
	if padding < 0 {
		padding += teaBlockSize
	}
	body := make([]byte, 0, padding+3+len(plain)+7)
	head := make([]byte, 1)
	if _, err := rand.Read(head); err != nil {
		return nil, fmt.Errorf("protocol: random padding: %w", err)
	}
	body = append(body, head[0]&0xF8|byte(padding))
	filler := make([]byte, padding+2)
	if _, err := rand.Read(filler); err != nil {
		return nil, fmt.Errorf("protocol: random padding: %w", err)
	}
	body = append(body, filler...)
	body = append(body, plain...)
	body = append(body, make([]byte, 7)...)

	return chain(body, key, false), nil
}

// Decrypt reverses Encrypt and verifies the padding the protocol mandates.
func Decrypt(ciphertext, key []byte) ([]byte, error) {
	if len(key) != teaKeySize {
		return nil, fmt.Errorf("protocol: key must be %d bytes, got %d", teaKeySize, len(key))
	}
	if len(ciphertext) < teaMinPlaintext || len(ciphertext)%teaBlockSize != 0 {
		return nil, ErrCiphertext
	}
	plain := chain(ciphertext, key, true)

	padding := int(plain[0] & 7)
	if len(plain) < padding+10 || !allZero(plain[len(plain)-7:]) {
		return nil, ErrCiphertext
	}
	return plain[padding+3 : len(plain)-7], nil
}

// chain walks the blocks. Each one is mixed with the previous block's intermediate
// value on the way in and the previous output on the way out, in the same order whether
// the direction is encryption or decryption.
func chain(data, key []byte, decrypt bool) []byte {
	previousMixed := make([]byte, teaBlockSize)
	previousOutput := make([]byte, teaBlockSize)
	out := make([]byte, 0, len(data))
	for pos := 0; pos < len(data); pos += teaBlockSize {
		block := data[pos : pos+teaBlockSize]
		if decrypt {
			mixed := teaBlock(xor(block, previousMixed), key, true)
			out = append(out, xor(mixed, previousOutput)...)
			previousMixed, previousOutput = mixed, block
			continue
		}
		mixed := xor(block, previousOutput)
		produced := xor(teaBlock(mixed, key, false), previousMixed)
		out = append(out, produced...)
		previousMixed, previousOutput = mixed, produced
	}
	return out
}

func xor(a, b []byte) []byte {
	out := make([]byte, len(a))
	for i := range a {
		out[i] = a[i] ^ b[i]
	}
	return out
}

func allZero(data []byte) bool {
	for _, b := range data {
		if b != 0 {
			return false
		}
	}
	return true
}
