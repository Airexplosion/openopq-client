package client

import (
	"context"
	"fmt"

	"openopq/internal/protocol"
)

// LoginCommand is the service a session is established through.
const LoginCommand = protocol.WTLoginCommand

// LoginInput is what the login needs beyond what the scan produced.
type LoginInput struct {
	// DeviceName is what the device block reports; empty uses the device's own.
	DeviceName string
	// Ticket106 is one of the login's two credential blocks. A confirmation carries one as
	// the enciphered A1, so an empty value here means that one.
	Ticket106 []byte
	// Ticket16A travels beside it. What the confirmation's blocks map onto it has not been
	// read out of anything, so it is supplied rather than derived — and an empty one is
	// refused rather than filled in with a guess.
	Ticket16A []byte
}

// Login runs the login that follows a scan.
//
// It is the same shape as a fetch — a body enciphered under the key the two sides agreed,
// inside a frame — and differs in two numbers: the service it is addressed to, and the
// command its frame carries. The body is the fifteen blocks the reference codec builds,
// which are the same fifteen the published implementations list, so nothing new is needed to
// make it.
//
// The session the exchange speaks for has to name the account by now: a code is fetched
// before there is one, and a login is not.
func (l *QRLogin) Login(ctx context.Context, scan ScanResult, input LoginInput) (protocol.Credentials, error) {
	if scan.Uin == 0 || len(scan.DeviceKey) == 0 {
		return protocol.Credentials{}, fmt.Errorf("client: the scan produced no account or device key")
	}
	device := l.device
	if input.DeviceName != "" {
		device.DeviceName = input.DeviceName
	}
	ticket106 := input.Ticket106
	if len(ticket106) == 0 {
		ticket106 = scan.Ticket
	}

	body, err := protocol.BuildWTLogin(protocol.WTLoginInput{
		Uin:        scan.Uin,
		AppID:      device.AppID,
		SubAppID:   device.SubAppID,
		Guid:       device.Guid,
		DeviceName: device.DeviceName,
		Ticket106:  ticket106,
		Ticket16A:  input.Ticket16A,
		TgtgtKey:   scan.DeviceKey,
	})
	if err != nil {
		return protocol.Credentials{}, err
	}
	// The body is handed to the OICQ layer as plaintext; BuildOICQ enciphers it once under the
	// agreed key. Enciphering here first would wrap it twice and the service would drop it.
	framed, err := protocol.BuildOICQ(protocol.OICQInput{
		Uin:       scan.Uin,
		PublicKey: l.keyPair.Public,
		ShareKey:  l.shareKey,
		Command:   protocol.OICQLoginCommand,
	}, body)
	if err != nil {
		return protocol.Credentials{}, fmt.Errorf("frame the login: %w", err)
	}

	// A login's body is already framed, and the service layer knows that by the command
	// alone — it adds no second length to this one.
	reply, err := l.exchange.Exchange(ctx, LoginCommand, framed)
	if err != nil {
		return protocol.Credentials{}, fmt.Errorf("send the login: %w", err)
	}
	inner, err := unwrapReplyFrame(reply, l.shareKey)
	if err != nil {
		return protocol.Credentials{}, err
	}
	return protocol.UnpackWTLogin(inner, scan.DeviceKey)
}

// unwrapReplyFrame takes a reply's body down to what it deciphered to.
//
// Whether a login's reply carries its frame behind a length the way a code's does has not
// been observed — no login's answer has ever been captured — so both readings are tried
// rather than one assumed. A frame cannot be mistaken for a bare body: the first begins with
// a length that the second would have to match by accident.
func unwrapReplyFrame(body, key []byte) ([]byte, error) {
	if frame, err := protocol.NewReader(body).LV32(); err == nil {
		if inner, err := protocol.UnwrapOICQReply(frame, key); err == nil {
			return inner, nil
		}
	}
	inner, err := protocol.UnwrapOICQReply(body, key)
	if err != nil {
		return nil, fmt.Errorf("unwrap the reply's frame: %w", err)
	}
	return inner, nil
}
