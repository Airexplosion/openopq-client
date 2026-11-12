package client

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"openopq/internal/protocol"
	"openopq/internal/sign"
)

// Signer signs one packet for the service.
type Signer interface {
	Sign(ctx context.Context, command string, seq uint32, packet []byte) (sign.Bundle, error)
}

// Connection is the part of a transport connection an exchange needs.
//
// Exchange is what a request goes through, rather than a bare send and a bare receive: the
// service interleaves answers to requests with messages no request asked for, and only the
// connection knows which is which.
type Connection interface {
	Send(packet []byte) error
	Receive(ctx context.Context) (protocol.SSOResponse, error)
	Exchange(ctx context.Context, packet []byte, sequence uint32) (protocol.SSOResponse, error)
}

// Session is what an exchange has to know about the account it speaks for.
//
// A QR code is fetched before there is an account, so most of this is empty on that path
// and filled in afterwards — which is exactly what the flow does, and why the same exchange
// serves both.
type Session struct {
	Uin   uint32
	AppID uint32
	MsfID uint32
	// Device is the sixteen-byte GUID written out as hex, which every request's header
	// reports. It is the same GUID the device blocks carry: a client whose two disagree is
	// claiming to be two machines, and one of them is the one the service remembers.
	Device string
	// NTSign travels in every request's extension and is the same for the life of the install.
	NTSign string
	// UID is the account's modern identifier, which the login yields and every post-login
	// request carries in extension field 16. Empty until there is a login.
	UID string
	// Framing is which of the two service layers this client's build sends.
	Framing protocol.Framing
	// Token, D2 and SessionKey are the login's, and are empty until there is one. Token is the
	// ticket a request is authenticated by, D2 is what the service layer carries, and
	// SessionKey is what the payload is enciphered under. A packet sent with the last two
	// empty is the pre-login shape, which is what a capture of that stage shows.
	Token      []byte
	D2         []byte
	SessionKey []byte
	// Now supplies the timestamp; nil uses the clock.
	Now func() time.Time
}

func (s Session) now() time.Time {
	if s.Now == nil {
		return time.Now()
	}
	return s.Now()
}

// ServiceExchange puts one request on a connection, signing it first.
//
// The sequence number is the service's own correlation handle and has to rise by one per
// request on a connection, so it is the one piece of state here and it is guarded.
type ServiceExchange struct {
	conn   Connection
	signer Signer
	// The session is held by pointer because the account it names is not known until a scan
	// says so, and a login has to carry it.
	session *Session

	mu          sync.Mutex
	seq         uint32
	authFailure atomic.Pointer[Refusal]
}

// NewServiceExchange wires a connection, a signer and an account together. The session is
// held rather than copied, so that filling the account in later reaches the packets.
func NewServiceExchange(conn Connection, signer Signer, session *Session) *ServiceExchange {
	e := &ServiceExchange{conn: conn, signer: signer, session: session}
	// The sequence a client's requests carry does not start at one. A capture shows a fresh
	// connection opening in the millions and rising by one per request, so it is seeded with
	// a value in that range rather than from zero; a request numbered one is the kind of thing
	// a service can single out. OPENOPQ_SEQ_BASE pins it for a reproducible run.
	if text := strings.TrimSpace(os.Getenv("OPENOPQ_SEQ_BASE")); text != "" {
		if v, err := strconv.ParseUint(text, 10, 32); err == nil {
			e.seq = uint32(v)
		}
	} else {
		var b [4]byte
		if _, err := rand.Read(b[:]); err == nil {
			e.seq = 0x3000000 + (binary.BigEndian.Uint32(b[:]) % 0x3000000)
		}
	}
	return e
}

// Exchange sends a request and returns the body of its reply.
func (e *ServiceExchange) Exchange(ctx context.Context, command string, body []byte) (answer []byte, resultErr error) {
	if e.conn == nil || e.signer == nil || e.session == nil {
		return nil, fmt.Errorf("client: the exchange is not wired to a connection and a signer")
	}
	e.mu.Lock()
	e.seq++
	seq := e.seq
	e.mu.Unlock()
	stage := "signing"
	trace := func(next string) { stage = next; traceHeartbeat(ctx, heartbeatTrace{stage: next, sequence: seq}) }
	defer func() {
		if resultErr != nil {
			traceHeartbeat(ctx, heartbeatTrace{stage: stage, sequence: seq, err: resultErr})
		}
	}()
	trace("signing")

	// The signer sees the packet as it will travel, which is why it is asked before the
	// request is framed rather than after.
	bundle, err := e.signer.Sign(ctx, command, seq, body)
	if err != nil {
		return nil, fmt.Errorf("sign %s: %w", command, err)
	}
	trace("signed")
	// Experimental: a real 7.0.2 client's trans_emp fetch carries no device token in its sign
	// block, but some signers return one. OPENOPQ_NO_TOKEN drops it to match the capture.
	if os.Getenv("OPENOPQ_NO_TOKEN") != "" {
		bundle.DeviceToken = nil
	}
	trace("framing")
	packet, err := protocol.BuildSSO(protocol.SSORequest{
		Sequence:    seq,
		AppID:       e.session.AppID,
		MsfID:       e.session.MsfID,
		Framing:     e.session.Framing,
		Token:       e.session.Token,
		Device:      e.session.Device,
		Uin:         e.session.Uin,
		Command:     command,
		NTSign:      e.session.NTSign,
		UID:         e.session.UID,
		Signature:   bundle.Sign,
		Extra:       bundle.Extra,
		DeviceToken: bundle.DeviceToken,
		D2:          e.session.D2,
		SessionKey:  e.session.SessionKey,
		Timestamp:   e.session.now().Unix(),
		Body:        body,
	})
	if err != nil {
		return nil, fmt.Errorf("frame %s: %w", command, err)
	}
	trace("sending")
	var reply protocol.SSOResponse
	if observed, ok := e.conn.(interface {
		ExchangeObserved(context.Context, []byte, uint32, func()) (protocol.SSOResponse, error)
	}); ok {
		reply, err = observed.ExchangeObserved(ctx, packet, seq, func() { trace("sent") })
	} else {
		// An injected transport cannot claim a confirmed write without this hook.
		reply, err = e.conn.Exchange(ctx, packet, seq)
	}
	if err != nil {
		return nil, fmt.Errorf("await %s: %w", command, err)
	}
	stage = "reply"
	traceHeartbeat(ctx, heartbeatTrace{stage: stage, sequence: seq, code: &reply.Code})
	if !reply.Accepted() {
		refusal := &Refusal{Command: command, Code: reply.Code, Message: reply.Message}
		if reply.Code == -10003 && len(e.session.SessionKey) > 0 {
			e.authFailure.Store(refusal)
		}
		return nil, refusal
	}
	return reply.Body, nil
}

// A live socket is not proof that the server still accepts its login tickets.
func (e *ServiceExchange) AuthenticationError() error {
	if failure := e.authFailure.Load(); failure != nil {
		return failure
	}
	return nil
}
func (e *ServiceExchange) ResetAuthenticationError() { e.authFailure.Store(nil) }

// NextSequence reports the sequence the next request will carry, for logging.
func (e *ServiceExchange) NextSequence() uint32 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.seq + 1
}

// Refusal is a request the service carried out a decision on rather than answered: the code
// is the service's, and the message usually says why.
type Refusal struct {
	Command string
	Code    int32
	Message string
}

func (r *Refusal) Error() string {
	if r.Message == "" {
		return fmt.Sprintf("the service refused %s with code %d", r.Command, r.Code)
	}
	return fmt.Sprintf("the service refused %s with code %d: %s", r.Command, r.Code, r.Message)
}
