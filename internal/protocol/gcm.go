package protocol

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
)

// gcmNonceSize is the nonce length every encrypted block in this protocol uses. It is
// written in front of the ciphertext, which is what makes the blocks self-describing.
const gcmNonceSize = 12

// sealWithGCM encrypts a block and puts the nonce in front of the ciphertext.
//
// An empty nonce is drawn from the system. Supplying one is what makes a packet
// reproducible, and it is only for tests: reusing a nonce with the same key breaks GCM
// outright, so nothing on a live path may pass one.
func sealWithGCM(plain, key, nonce []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	if len(nonce) == 0 {
		nonce = make([]byte, gcmNonceSize)
		if _, err := rand.Read(nonce); err != nil {
			return nil, fmt.Errorf("generate nonce: %w", err)
		}
	}
	if len(nonce) != gcmNonceSize {
		return nil, fmt.Errorf("protocol: nonce is %d bytes, want %d", len(nonce), gcmNonceSize)
	}
	return gcm.Seal(nonce, nonce, plain, nil), nil
}

// openWithGCM reverses sealWithGCM. A block that fails to authenticate is an error and
// never a value, so a tampered or truncated packet cannot be mistaken for a good one.
func openWithGCM(sealed, key []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	if len(sealed) < gcmNonceSize+gcm.Overhead() {
		return nil, fmt.Errorf("%w: encrypted block is only %d bytes",
			ErrShortPacket, len(sealed))
	}
	return gcm.Open(nil, sealed[:gcmNonceSize], sealed[gcmNonceSize:], nil)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("protocol: AES key: %w", err)
	}
	return cipher.NewGCM(block)
}
