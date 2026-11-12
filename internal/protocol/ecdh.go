package protocol

import (
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
)

// KeyExchangeCommand is the service the key exchange is addressed to.
const KeyExchangeCommand = "trpc.login.ecdh.EcdhService.SsoKeyExchange"

// Constants belonging to the key exchange.
//
// Both of these are protocol constants, not credentials: they are compiled into every
// copy of the client and are the same for every account. The server's key is a public key
// by construction, and the proof key only protects a digest that the same shipped client
// produces. They cannot move to configuration, because a client that sends anything else
// is not speaking the protocol.
var (
	// serverKey is the server's static public key, in the SEC1 uncompressed form the
	// exchange uses on the curve the protocol calls P256.
	serverKey = mustDecodeHex("049D1423332735980EDABE7E9EA451B3395B6F35250DB8FC56F25889" +
		"F628CBAE3E8E73077914071EEEBC108F4E0170057792BB17AA303AF652313D17C1AC815E79")
	// proofKey protects the digest the client sends back to show it derived the same
	// secret. It is symmetric and fixed, so it authenticates the exchange only against
	// accidents, not against anyone who has read a client binary.
	proofKey = mustDecodeHex("e2733bf403149913cbf80c7a95168bd4ca6935ee53cd39764beebe2e007e3aee")
)

// Tags of a key exchange message. The response reuses one of the request's numbers for a
// different field, so the two sets are named apart.
const (
	kexRequestKey    = 1
	kexRequestProof  = 5
	kexResponseBlock = 1
	kexResponseKey   = 3
	// kexSecretKey and kexSecretToken are inside the block the response enciphers.
	kexSecretKey   = 1
	kexSecretToken = 2
	// kexExchangeMarker is the constant the request sends as its second field.
	kexExchangeMarker = 1
	// kexHashMarker is the zero the digest is salted with.
	kexHashMarker = 0
)

// ssoKeySizes are the lengths the service accepts for the established key. The reference
// allows all three.
var ssoKeySizes = []int{16, 24, 32}

// mustDecodeHex turns a literal into bytes at start-up. The literals above are compile
// time values, so a failure here can only mean one was mistyped.
func mustDecodeHex(text string) []byte {
	value, err := hex.DecodeString(text)
	if err != nil {
		panic("protocol: malformed constant: " + err.Error())
	}
	return value
}

// KeyExchange is what a request to establish a shared secret is built from.
type KeyExchange struct {
	Uin  uint32
	Guid []byte
	// Timestamp travels in the clear and is hashed into the proof.
	Timestamp uint32
	// PrivateKey is the client's ephemeral scalar. An empty one is drawn from the system;
	// supplying one is what makes the request reproducible.
	PrivateKey []byte
	// BodyNonce and ProofNonce fill the request's two enciphered blocks. Empty ones are
	// drawn from the system. Like PrivateKey, these are for tests only.
	BodyNonce  []byte
	ProofNonce []byte
}

// KeyExchangeRequest is a built request together with the key needed to read the answer.
// The two travel as a pair: the response cannot be deciphered without the private half,
// and it is discarded after a single exchange.
type KeyExchangeRequest struct {
	Body       []byte
	PrivateKey []byte
}

// SSOSecret is what the exchange establishes: the key the NT login body is enciphered
// with, and the token that identifies the exchange.
type SSOSecret struct {
	Key   []byte
	Token []byte
}

// BuildKeyExchange encodes a request to establish a shared secret.
//
// The request is a Diffie-Hellman public key with a proof: the same secret the client
// derives is also used to encipher a digest over the request, so the server can tell a
// client that actually performed the agreement from one that copied a packet.
func BuildKeyExchange(input KeyExchange) (KeyExchangeRequest, error) {
	private, err := clientKey(input.PrivateKey)
	if err != nil {
		return KeyExchangeRequest{}, err
	}
	server, err := ecdh.P256().NewPublicKey(serverKey)
	if err != nil {
		return KeyExchangeRequest{}, fmt.Errorf("server key: %w", err)
	}
	shared, err := private.ECDH(server)
	if err != nil {
		return KeyExchangeRequest{}, fmt.Errorf("key agreement: %w", err)
	}

	public := private.PublicKey().Bytes()
	payload := Marshal(
		BytesField(kexRequestKey, []byte(strconv.FormatUint(uint64(input.Uin), 10))),
		BytesField(2, input.Guid),
	)
	enciphered, err := sealWithGCM(payload, shared, input.BodyNonce)
	if err != nil {
		return KeyExchangeRequest{}, fmt.Errorf("encipher key exchange payload: %w", err)
	}

	proof, err := sealWithGCM(proofDigest(public, enciphered, input.Timestamp),
		proofKey, input.ProofNonce)
	if err != nil {
		return KeyExchangeRequest{}, fmt.Errorf("encipher proof: %w", err)
	}

	return KeyExchangeRequest{
		Body: Marshal(
			BytesField(kexRequestKey, public),
			VarintField(2, kexExchangeMarker),
			BytesField(3, enciphered),
			VarintField(4, uint64(input.Timestamp)),
			BytesField(kexRequestProof, proof),
		),
		PrivateKey: private.Bytes(),
	}, nil
}

// proofDigest hashes what the proof covers: the public key, a constant, the enciphered
// payload, a zero, and the timestamp.
func proofDigest(public, enciphered []byte, timestamp uint32) []byte {
	hashed := make([]byte, 0, len(public)+len(enciphered)+16)
	hashed = append(hashed, public...)
	hashed = append(hashed, U32(1)...)
	hashed = append(hashed, enciphered...)
	hashed = append(hashed, U32(kexHashMarker)...)
	hashed = append(hashed, U32(timestamp)...)
	digest := sha256.Sum256(hashed)
	return digest[:]
}

// stripSSOBodyLength drops the four-byte big-endian length that an SSO reply body opens with.
// The length counts itself, so the payload is the bytes between it and the length it names; a
// body that does not open with a credible length (too short, or a length that overruns) is
// returned unchanged so an already-unwrapped sample still parses.
func stripSSOBodyLength(body []byte) []byte {
	if len(body) < 4 {
		return body
	}
	length := int(body[0])<<24 | int(body[1])<<16 | int(body[2])<<8 | int(body[3])
	if length < 4 || length > len(body) {
		return body
	}
	return body[4:length]
}

// UnpackKeyExchange reads the answer and recovers the secret.
//
// The private key is the one BuildKeyExchange returned; nothing else can open the block,
// so a response that arrives without its request being remembered is not recoverable.
func UnpackKeyExchange(response, privateKey []byte) (SSOSecret, error) {
	// An SSO reply body opens with a four-byte length that counts itself, left in place by the
	// frame layer for each command's own parser to strip — the trans_emp parser consumes it with
	// its leading word, and the key-exchange protobuf sits directly behind it. Drop it before the
	// protobuf is read; without this the first byte read is the high byte of that length (zero),
	// which the protobuf reader rejects as "field tag 0".
	response = stripSSOBodyLength(response)
	fields, err := Unmarshal(response)
	if err != nil {
		return SSOSecret{}, fmt.Errorf("key exchange response: %w", err)
	}
	private, err := clientKey(privateKey)
	if err != nil {
		return SSOSecret{}, err
	}
	serverBytes, err := bytesField(fields, kexResponseKey)
	if err != nil {
		return SSOSecret{}, fmt.Errorf("key exchange response: %w", err)
	}
	server, err := ecdh.P256().NewPublicKey(serverBytes)
	if err != nil {
		return SSOSecret{}, fmt.Errorf("server key: %w", err)
	}
	shared, err := private.ECDH(server)
	if err != nil {
		return SSOSecret{}, fmt.Errorf("key agreement: %w", err)
	}

	enciphered, err := bytesField(fields, kexResponseBlock)
	if err != nil {
		return SSOSecret{}, fmt.Errorf("key exchange response: %w", err)
	}
	plain, err := openWithGCM(enciphered, shared)
	if err != nil {
		return SSOSecret{}, fmt.Errorf("key exchange response does not authenticate: %w", err)
	}
	secret, err := Unmarshal(plain)
	if err != nil {
		return SSOSecret{}, fmt.Errorf("key exchange contents: %w", err)
	}
	key, err := bytesField(secret, kexSecretKey)
	if err != nil {
		return SSOSecret{}, fmt.Errorf("key exchange contents: %w", err)
	}
	token, err := bytesField(secret, kexSecretToken)
	if err != nil {
		return SSOSecret{}, fmt.Errorf("key exchange contents: %w", err)
	}
	if !isSSOKeySize(len(key)) || len(token) == 0 {
		return SSOSecret{}, fmt.Errorf("key exchange yielded an unusable key or token")
	}
	return SSOSecret{Key: copyBytes(key), Token: copyBytes(token)}, nil
}

// clientKey turns a scalar into a private key, drawing one when none is given.
func clientKey(scalar []byte) (*ecdh.PrivateKey, error) {
	if len(scalar) == 0 {
		generated, err := ecdh.P256().GenerateKey(rand.Reader)
		if err != nil {
			return nil, fmt.Errorf("generate key exchange key: %w", err)
		}
		return generated, nil
	}
	private, err := ecdh.P256().NewPrivateKey(scalar)
	if err != nil {
		return nil, fmt.Errorf("client key: %w", err)
	}
	return private, nil
}

func isSSOKeySize(size int) bool {
	for _, allowed := range ssoKeySizes {
		if size == allowed {
			return true
		}
	}
	return false
}
