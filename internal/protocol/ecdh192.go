package protocol

import (
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math/big"
	"os"
	"strings"
)

// The QR flow agrees a key per connection before it can build a body. The published NTQQ
// implementations describe it exactly: an ECDH agreement on the curve the protocol calls
// secp192k1, whose X coordinate, hashed with MD5, is the key its TEA enciphering uses.
//
// That is why no fixed key opens a captured frame and why none was sitting in memory to be
// found: it is derived, per connection, from a secret that never leaves the client. What
// *is* fixed is the service's own public key, which every client carries.
//
// The curve is small enough to carry its own arithmetic rather than reach for a dependency,
// and doing so keeps the constants visible next to the one place they are used.
const (
	// secp192k1Size is the field width in bytes, which is also the width of a coordinate
	// and of the secret.
	secp192k1Size = 24
	// secp192k1PublicSize is the length of a compressed point: a parity byte and an X.
	secp192k1PublicSize = secp192k1Size + 1
	// secp192k1UncompressedSize is the length of an uncompressed point.
	secp192k1UncompressedSize = secp192k1Size*2 + 1
)

// The curve's parameters. secp192k1 is a published SECG curve; these are its constants, the
// same ones every implementation carries.
var (
	secp192k1P  = mustBigHex("FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEFFFFEE37")
	secp192k1A  = big.NewInt(0)
	secp192k1B  = big.NewInt(3)
	secp192k1N  = mustBigHex("FFFFFFFFFFFFFFFFFFFFFFFE26F2FC170F69466A74DEFD8D")
	secp192k1Gx = mustBigHex("DB4FF10EC057E9AE26B07D0280B7F4341DA5D1B1EAE06C7D")
	secp192k1Gy = mustBigHex("9B2F2F6D9C5628A7844163D015BE86344082AA88D95E2F9D")
	secp192k1G  = affinePoint{x: secp192k1Gx, y: secp192k1Gy}
)

// ServerECDHKey is the service's static public key for the QR flow, in the uncompressed
// form. It is a public key, and the same in every copy of the client: the agreement is what
// is secret, not this.
//
// This is the key every published reimplementation carries, and QQ 7.0.2 uses it too: a
// fetch QQ 7.0.2 sent was deciphered with the share key this key agrees (confirmed by
// extracting that client's own ephemeral private key and reproducing its share key, which
// opened its enciphered trans_emp body). The key is not stored as these plaintext bytes in
// the 7.0.2 bundle — it is held in another form — which is why a byte search for it comes up
// empty; that absence is not evidence of a rotation. OPENOPQ_SERVER_ECDH_KEY overrides it for
// a deployment that reads its own off a client.
var ServerECDHKey = serverECDHKey()

func serverECDHKey() []byte {
	if text := strings.TrimSpace(os.Getenv("OPENOPQ_SERVER_ECDH_KEY")); text != "" {
		if raw, err := hex.DecodeString(text); err == nil &&
			(len(raw) == secp192k1PublicSize || len(raw) == secp192k1UncompressedSize) {
			return raw
		}
	}
	return mustDecodeHex(
		"04928D8850673088B343264E0C6BACB8496D697799F37211DEB25BB73906CB089FEA" +
			"9639B4E0260498B51A992D50813DA8")
}

// mustBigHex turns a hex literal into a big integer at start-up.
func mustBigHex(text string) *big.Int {
	value, ok := new(big.Int).SetString(text, 16)
	if !ok {
		panic("protocol: malformed curve constant")
	}
	return value
}

// affinePoint is a point in affine coordinates. A nil x means the point at infinity, which
// is the group's identity and the result of adding a point to its own inverse.
type affinePoint struct {
	x, y *big.Int
}

func (p affinePoint) atInfinity() bool { return p.x == nil }

func infinity() affinePoint { return affinePoint{} }

// onCurve reports whether a point satisfies y² = x³ + ax + b, which every point the service
// sends has to, and which is how a public key is validated before it is used.
func onCurve(p affinePoint) bool {
	if p.atInfinity() {
		return false
	}
	if p.x.Sign() < 0 || p.x.Cmp(secp192k1P) >= 0 {
		return false
	}
	if p.y.Sign() < 0 || p.y.Cmp(secp192k1P) >= 0 {
		return false
	}
	left := modMul(p.y, p.y)
	right := modAdd(modAdd(modMul(modMul(p.x, p.x), p.x), modMul(secp192k1A, p.x)), secp192k1B)
	return left.Cmp(right) == 0
}

func modAdd(a, b *big.Int) *big.Int { return new(big.Int).Mod(new(big.Int).Add(a, b), secp192k1P) }
func modSub(a, b *big.Int) *big.Int { return new(big.Int).Mod(new(big.Int).Sub(a, b), secp192k1P) }
func modMul(a, b *big.Int) *big.Int { return new(big.Int).Mod(new(big.Int).Mul(a, b), secp192k1P) }

// modDiv divides modulo p. The curve's order is prime, so every non-zero denominator has an
// inverse.
func modDiv(a, b *big.Int) (*big.Int, error) {
	inverse := new(big.Int).ModInverse(b, secp192k1P)
	if inverse == nil {
		return nil, fmt.Errorf("protocol: no inverse for %s on the curve", b)
	}
	return modMul(a, inverse), nil
}

// pointAdd adds two points. Adding a point to its own inverse gives the identity rather than
// a coordinate, which is the case a division would otherwise fail on.
func pointAdd(p, q affinePoint) (affinePoint, error) {
	if p.atInfinity() {
		return q, nil
	}
	if q.atInfinity() {
		return p, nil
	}

	var slope *big.Int
	var err error
	if p.x.Cmp(q.x) == 0 {
		if p.y.Cmp(q.y) != 0 || p.y.Sign() == 0 {
			return infinity(), nil // a vertical line, so the sum is the identity
		}
		// Doubling: the tangent is (3x² + a) / 2y.
		numerator := modAdd(modMul(big.NewInt(3), modMul(p.x, p.x)), secp192k1A)
		slope, err = modDiv(numerator, modMul(big.NewInt(2), p.y))
	} else {
		slope, err = modDiv(modSub(q.y, p.y), modSub(q.x, p.x))
	}
	if err != nil {
		return infinity(), err
	}

	x := modSub(modSub(modMul(slope, slope), p.x), q.x)
	y := modSub(modMul(slope, modSub(p.x, x)), p.y)
	return affinePoint{x: x, y: y}, nil
}

// scalarMult multiplies a point by a scalar, by double and add.
func scalarMult(k *big.Int, p affinePoint) (affinePoint, error) {
	result := infinity()
	addend := p
	for i := 0; i < k.BitLen(); i++ {
		if k.Bit(i) == 1 {
			var err error
			result, err = pointAdd(result, addend)
			if err != nil {
				return infinity(), err
			}
		}
		var err error
		addend, err = pointAdd(addend, addend)
		if err != nil {
			return infinity(), err
		}
	}
	return result, nil
}

// fixedWidth renders a value as exactly n big-endian bytes, which is how a coordinate
// travels.
func fixedWidth(value *big.Int, n int) []byte {
	out := make([]byte, n)
	raw := value.Bytes()
	if len(raw) > n {
		raw = raw[len(raw)-n:]
	}
	copy(out[n-len(raw):], raw)
	return out
}

// parsePublicKey reads a point in either encoding the protocol uses: a compressed point is a
// parity byte and an X, and an uncompressed one carries both coordinates.
func parsePublicKey(data []byte) (affinePoint, error) {
	switch len(data) {
	case secp192k1PublicSize:
		if data[0] != 0x02 && data[0] != 0x03 {
			return infinity(), fmt.Errorf("protocol: compressed point opens with %#02x", data[0])
		}
		x := new(big.Int).SetBytes(data[1:])
		// y² = x³ + ax + b, and p ≡ 3 (mod 4) so the root is right^((p+1)/4).
		right := modAdd(modAdd(modMul(modMul(x, x), x), modMul(secp192k1A, x)), secp192k1B)
		exponent := new(big.Int).Rsh(new(big.Int).Add(secp192k1P, big.NewInt(1)), 2)
		y := new(big.Int).Exp(right, exponent, secp192k1P)
		wantEven := data[0] == 0x02
		if (y.Bit(0) == 0) != wantEven {
			y = modSub(secp192k1P, y)
		}
		point := affinePoint{x: x, y: y}
		if !onCurve(point) {
			return infinity(), fmt.Errorf("protocol: recovered point is not on the curve")
		}
		return point, nil
	case secp192k1UncompressedSize:
		if data[0] != 0x04 {
			return infinity(), fmt.Errorf("protocol: uncompressed point opens with %#02x", data[0])
		}
		point := affinePoint{
			x: new(big.Int).SetBytes(data[1 : 1+secp192k1Size]),
			y: new(big.Int).SetBytes(data[1+secp192k1Size:]),
		}
		if !onCurve(point) {
			return infinity(), fmt.Errorf("protocol: point is not on the curve")
		}
		return point, nil
	default:
		return infinity(), fmt.Errorf("protocol: point is %d bytes, want %d or %d",
			len(data), secp192k1PublicSize, secp192k1UncompressedSize)
	}
}

// packPublicKey renders a point in the compressed form the protocol sends: the parity byte
// and the X coordinate, twenty-five bytes.
func packPublicKey(p affinePoint) ([]byte, error) {
	if p.atInfinity() {
		return nil, fmt.Errorf("protocol: the point at infinity cannot be packed")
	}
	out := make([]byte, secp192k1PublicSize)
	out[0] = 0x02
	if p.y.Bit(0) == 1 {
		out[0] = 0x03
	}
	copy(out[1:], fixedWidth(p.x, secp192k1Size))
	return out, nil
}

// QRKeyPair is the client's half of the agreement: a secret and the public point it
// produces.
type QRKeyPair struct {
	Private []byte
	Public  []byte
}

// NewQRKeyPair draws a fresh key pair. The client draws one per connection, which is why a
// captured frame cannot be opened later.
func NewQRKeyPair() (QRKeyPair, error) {
	order := new(big.Int).Sub(secp192k1N, big.NewInt(1))
	for attempt := 0; attempt < 16; attempt++ {
		raw := make([]byte, secp192k1Size)
		if _, err := rand.Read(raw); err != nil {
			return QRKeyPair{}, fmt.Errorf("draw a key: %w", err)
		}
		secret := new(big.Int).Mod(new(big.Int).SetBytes(raw), order)
		secret.Add(secret, big.NewInt(1))
		point, err := scalarMult(secret, secp192k1G)
		if err != nil {
			return QRKeyPair{}, err
		}
		public, err := packPublicKey(point)
		if err != nil {
			return QRKeyPair{}, err
		}
		return QRKeyPair{Private: fixedWidth(secret, secp192k1Size), Public: public}, nil
	}
	return QRKeyPair{}, fmt.Errorf("protocol: could not draw a usable key pair")
}

// QRShareKey agrees the key the body is enciphered with: the X coordinate of the shared
// point, hashed with MD5, which is sixteen bytes and exactly what TEA takes.
func QRShareKey(private, peerPublic []byte) ([]byte, error) {
	if len(private) != secp192k1Size {
		return nil, fmt.Errorf("protocol: secret is %d bytes, want %d", len(private), secp192k1Size)
	}
	secret := new(big.Int).SetBytes(private)
	if secret.Sign() == 0 || secret.Cmp(secp192k1N) >= 0 {
		return nil, fmt.Errorf("protocol: secret is outside the curve's order")
	}
	peer, err := parsePublicKey(peerPublic)
	if err != nil {
		return nil, err
	}
	shared, err := scalarMult(secret, peer)
	if err != nil {
		return nil, err
	}
	if shared.atInfinity() {
		return nil, fmt.Errorf("protocol: the agreement produced the identity")
	}
	digest := md5.Sum(fixedWidth(shared.x, secp192k1Size))
	return digest[:], nil
}
