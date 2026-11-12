// Package client drives the login protocols over a connection.
//
// It is what sits between the API layer — which only knows bot.Bot — and the protocol
// layer, which only knows bytes. The one thing it asks for is a way to put a request on the
// wire and get a reply's body back, so the state machines here can be exercised without a
// connection and the signing and framing stay where they belong.
package client

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"openopq/internal/protocol"
)

// ErrScanTimeout reports a code that lapsed before anyone scanned it.
var ErrScanTimeout = errors.New("client: the code lapsed before it was scanned")

// Exchange puts one service request on the wire and returns the body of its reply.
//
// The body it takes is what the service layer wraps; the body it returns is that wrapper
// taken off again. Everything between — the signing, the connection, the cipher around the
// service frame — is somebody else's business.
type Exchange interface {
	Exchange(ctx context.Context, command string, body []byte) ([]byte, error)
}

// The flow's own timings. A code lapses after two minutes, which the service reports as
// expire_time, and it is worth checking a little faster than it takes a person to pick up a
// phone.
const (
	DefaultPollInterval = 1500 * time.Millisecond
	DefaultScanLifetime = 120 * time.Second
)

// QRCode is a code waiting to be scanned.
type QRCode struct {
	// Signature identifies the code for the polls that follow it.
	Signature []byte
	// URL is what the code encodes.
	URL string
	// Image is the code as a PNG, ready to hand to whoever shows it.
	Image []byte
}

// ScanResult is what a scan produced.
type ScanResult struct {
	// Uin is the account that scanned.
	Uin uint32
	// DeviceKey is what the login that follows is enciphered with.
	DeviceKey []byte
	// Ticket is the enciphered credential the login hands back.
	Ticket []byte
	// NoPicSig travels with the confirmation.
	NoPicSig []byte
}

// QRLoginOptions are the parts of the flow a caller may want to pin, which is mostly what
// makes it testable: the clock and the wait are here so a test does not sit out two real
// minutes.
type QRLoginOptions struct {
	Poll     time.Duration
	Lifetime time.Duration
	Now      func() time.Time
	Sleep    func(context.Context, time.Duration) error
}

func (o QRLoginOptions) withDefaults() QRLoginOptions {
	if o.Poll <= 0 {
		o.Poll = DefaultPollInterval
	}
	if o.Lifetime <= 0 {
		o.Lifetime = DefaultScanLifetime
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Sleep == nil {
		o.Sleep = sleep
	}
	return o
}

// sleep waits unless the caller gives up first.
func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// QRLogin drives a scan: it asks for a code, watches it, and hands back what the
// confirmation carries.
//
// The key it agrees is made once and used for the whole exchange, which is what the flow
// does: the request's public key and every body that follows travel under one agreement.
type QRLogin struct {
	device   protocol.QRDevice
	exchange Exchange
	options  QRLoginOptions

	keyPair  protocol.QRKeyPair
	shareKey []byte
}

// NewQRLogin agrees a key and returns a machine ready to fetch.
func NewQRLogin(exchange Exchange, device protocol.QRDevice, options QRLoginOptions) (*QRLogin, error) {
	if exchange == nil {
		return nil, fmt.Errorf("client: no exchange to send through")
	}
	pair, err := protocol.NewQRKeyPair()
	if err != nil {
		return nil, err
	}
	key, err := protocol.QRShareKey(pair.Private, protocol.ServerECDHKey)
	if err != nil {
		return nil, err
	}
	if path := os.Getenv("OPENOPQ_DUMP_SHAREKEY"); path != "" {
		_ = os.WriteFile(path, []byte(hex.EncodeToString(key)), 0o644)
	}
	return &QRLogin{
		device:   device,
		exchange: exchange,
		options:  options.withDefaults(),
		keyPair:  pair,
		shareKey: key,
	}, nil
}

// Fetch asks the service for a code.
func (l *QRLogin) Fetch(ctx context.Context) (QRCode, error) {
	reply, err := l.call(ctx, protocol.QRSubCommandFetch, protocol.BuildQRFetchTLVs(l.device))
	if err != nil {
		return QRCode{}, err
	}
	parsed, err := protocol.ParseQRFetchReply(reply)
	if err != nil {
		return QRCode{}, err
	}
	if os.Getenv("OPENOPQ_POLL_DEBUG") != "" {
		fmt.Fprintf(os.Stderr, "[fetch] qr url=%q sig=%s\n", parsed.URL, hex.EncodeToString(parsed.Signature))
	}
	return QRCode{Signature: parsed.Signature, URL: parsed.URL, Image: parsed.Image}, nil
}

// Wait polls a code until the account approves it, and returns what the confirmation
// carries.
//
// It gives up when the caller's context does or when the code's own lifetime runs out,
// whichever comes first: a code that has lapsed will never be confirmed, and polling it
// after that is only noise.
func (l *QRLogin) Wait(ctx context.Context, code QRCode) (ScanResult, error) {
	if len(code.Signature) == 0 {
		return ScanResult{}, fmt.Errorf("client: the code carries no signature to poll")
	}
	deadline := l.options.Now().Add(l.options.Lifetime)
	transientTimeouts := 0

	for {
		now := l.options.Now()
		if !now.Before(deadline) {
			return ScanResult{}, ErrScanTimeout
		}
		// Polling is read-only. A lost response must not immediately invalidate a
		// still-valid QR code. Bound each attempt and retry only transient timeouts;
		// authentication/decoder errors and caller cancellation are never retried.
		remaining := deadline.Sub(now)
		attempt := 8 * time.Second
		if remaining < attempt {
			attempt = remaining
		}
		pollContext, stop := context.WithTimeout(ctx, attempt)
		state, err := l.poll(pollContext, code)
		stop()
		if err != nil {
			if ctx.Err() != nil {
				return ScanResult{}, ctx.Err()
			}
			if errors.Is(err, context.DeadlineExceeded) && l.options.Now().Before(deadline) && transientTimeouts < 2 {
				transientTimeouts++
				if err := l.options.Sleep(ctx, l.options.Poll); err != nil {
					return ScanResult{}, err
				}
				continue
			}
			return ScanResult{}, err
		}
		transientTimeouts = 0
		if !l.options.Now().Before(deadline) {
			return ScanResult{}, ErrScanTimeout
		}
		if state.Confirmed {
			if len(state.Tgtgt) == 0 {
				return ScanResult{}, fmt.Errorf("client: the scan was confirmed without a device key")
			}
			return ScanResult{
				Uin:       state.Uin,
				DeviceKey: state.Tgtgt,
				Ticket:    state.Ticket,
				NoPicSig:  state.NoPicSig,
			}, nil
		}
		if !l.options.Now().Before(deadline) {
			return ScanResult{}, ErrScanTimeout
		}
		if err := l.options.Sleep(ctx, l.options.Poll); err != nil {
			return ScanResult{}, err
		}
	}
}

// poll asks the service once whether the code has been read.
func (l *QRLogin) poll(ctx context.Context, code QRCode) (protocol.QRScanState, error) {
	reply, err := l.call(ctx, protocol.QRSubCommandPoll,
		protocol.BuildQRPoll(code.Signature))
	if err != nil {
		return protocol.QRScanState{}, err
	}
	state, perr := protocol.ParseQRScanState(reply)
	// Diagnostic: OPENOPQ_POLL_DEBUG logs the scan state byte on every poll, so a scan that
	// registers server-side (the state changes from "waiting") is visible even when the phone
	// never confirms. The whole payload is logged for any non-trivial state so a rejection
	// reason that travels in it can be read.
	if os.Getenv("OPENOPQ_POLL_DEBUG") != "" {
		if len(reply) > 1 {
			fmt.Fprintf(os.Stderr, "[poll] state=0x%02x confirmed=%v payloadlen=%d payload=%s\n",
				state.State, state.Confirmed, len(reply), hex.EncodeToString(reply))
		} else {
			fmt.Fprintf(os.Stderr, "[poll] state=0x%02x confirmed=%v payloadlen=%d\n",
				state.State, state.Confirmed, len(reply))
		}
	}
	return state, perr
}

// call runs one request: it builds the body, enciphers it under the agreed key, frames it,
// sends it, and unwraps the reply down to the sub-command's own payload.
func (l *QRLogin) call(ctx context.Context, subCommand uint32, blocks []byte) ([]byte, error) {
	// The record is built as plaintext here and enciphered once, by the OICQ layer below.
	body := protocol.BuildTransEmp(protocol.TransEmpBody{
		SubCommand: subCommand,
		AppID:      l.device.AppID,
		SubAppID:   l.device.SubAppID,
		Timestamp:  uint32(l.options.Now().Unix()),
		TLVs:       blocks,
	})
	framed, err := protocol.BuildOICQ(protocol.OICQInput{
		Uin:       0, // a code is fetched before there is an account
		PublicKey: l.keyPair.Public,
		ShareKey:  l.shareKey,
	}, body)
	if err != nil {
		return nil, fmt.Errorf("frame the request: %w", err)
	}

	// Experimental isolation hook: replace the built OICQ fetch body with a captured real
	// client's one (hex in the named file), so the signer signs that body instead. It isolates
	// whether a dropped fetch is the signature's fault or the built body's: a real body plus our
	// signer that the server accepts means the body is the suspect.
	if path := os.Getenv("OPENOPQ_REPLAY_FRAMED"); path != "" && subCommand == protocol.QRSubCommandFetch {
		if raw, err := os.ReadFile(path); err == nil {
			if decoded, err := hex.DecodeString(strings.TrimSpace(string(raw))); err == nil {
				framed = decoded
			}
		}
	}

	reply, err := l.exchange.Exchange(ctx, protocol.TransEmpCommand, framed)
	if err != nil {
		return nil, fmt.Errorf("send the request: %w", err)
	}
	// The reply's body is the frame, length-prefixed in turn.
	frame, err := protocol.NewReader(reply).LV32()
	if err != nil {
		return nil, fmt.Errorf("the reply is not a frame: %w", err)
	}
	inner, err := protocol.UnwrapOICQReply(frame, l.shareKey)
	if err != nil {
		return nil, fmt.Errorf("unwrap the reply's frame: %w", err)
	}
	unwrapped, err := protocol.UnwrapTransEmpReply(inner)
	if err != nil {
		return nil, err
	}
	if unwrapped.Command != uint32(uint16(subCommand)) {
		return nil, fmt.Errorf("client: the reply answers sub-command %d, not %d",
			unwrapped.Command, subCommand)
	}
	return unwrapped.Payload, nil
}
