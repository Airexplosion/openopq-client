package client

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"openopq/internal/protocol"
	"openopq/internal/sign"
)

type signingCall struct {
	command string
	seq     uint32
	body    []byte
}

type fakeSigner struct {
	calls []signingCall
	err   error
}

func (f *fakeSigner) Sign(ctx context.Context, command string, seq uint32, packet []byte) (sign.Bundle, error) {
	if f.err != nil {
		return sign.Bundle{}, f.err
	}
	f.calls = append(f.calls, signingCall{command: command, seq: seq, body: append([]byte(nil), packet...)})
	return sign.Bundle{Sign: []byte{0xAA}}, nil
}

type fakeConn struct {
	sent    [][]byte
	replies []protocol.SSOResponse
	err     error
}

func (f *fakeConn) Send(packet []byte) error {
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, append([]byte(nil), packet...))
	return nil
}

func (f *fakeConn) Exchange(ctx context.Context, packet []byte, sequence uint32) (protocol.SSOResponse, error) {
	if err := f.Send(packet); err != nil {
		return protocol.SSOResponse{}, err
	}
	return f.Receive(ctx)
}

func (f *fakeConn) Receive(ctx context.Context) (protocol.SSOResponse, error) {
	if f.err != nil {
		return protocol.SSOResponse{}, f.err
	}
	if len(f.replies) == 0 {
		return protocol.SSOResponse{}, errors.New("no reply was scripted")
	}
	next := f.replies[0]
	f.replies = f.replies[1:]
	return next, nil
}

// session returns a pointer, which is what the exchange holds.
func session() *Session {
	return &Session{
		AppID:  537352498,
		MsfID:  2052,
		Device: "4cd38b53fde08f7297cd8d76a81f1cec",
		NTSign: "00-abc-def-01",
		Now:    func() time.Time { return time.Unix(1791722167, 0) },
	}
}

// The service correlates by the sequence number, so it has to rise by exactly one per
// request and the signer has to see the same number the packet carries.
func TestExchangeNumbersRequestsInOrder(t *testing.T) {
	// The sequence is otherwise seeded randomly in the millions; pin it so the ordering this
	// test asserts (1, 2, 3, then 4) is reproducible.
	t.Setenv("OPENOPQ_SEQ_BASE", "0")
	conn := &fakeConn{replies: []protocol.SSOResponse{
		{Code: 0, Body: []byte{1}}, {Code: 0, Body: []byte{2}}, {Code: 0, Body: []byte{3}},
	}}
	signer := &fakeSigner{}
	exchange := NewServiceExchange(conn, signer, session())

	for i := 0; i < 3; i++ {
		if _, err := exchange.Exchange(context.Background(), protocol.TransEmpCommand, []byte{0x10}); err != nil {
			t.Fatalf("Exchange %d: %v", i, err)
		}
	}
	if len(signer.calls) != 3 || len(conn.sent) != 3 {
		t.Fatalf("%d signatures and %d packets, want three of each", len(signer.calls), len(conn.sent))
	}
	for i, call := range signer.calls {
		if call.seq != uint32(i+1) {
			t.Errorf("signature %d carried sequence %d", i, call.seq)
		}
		if call.command != protocol.TransEmpCommand {
			t.Errorf("signature %d was for %q", i, call.command)
		}
		// The signature is over the packet as it will travel, so the body has to be the
		// request's own.
		if !bytes.Equal(call.body, []byte{0x10}) {
			t.Errorf("signature %d saw body %x", i, call.body)
		}
	}
	if exchange.NextSequence() != 4 {
		t.Errorf("the next sequence is %d, want 4", exchange.NextSequence())
	}
}

// What the connection is handed has to be a service request, not a bare body: a caller that
// sent the body raw would get nowhere.
func TestExchangeSendsAServiceRequest(t *testing.T) {
	conn := &fakeConn{replies: []protocol.SSOResponse{{Code: 0, Body: []byte{0x11}}}}
	exchange := NewServiceExchange(conn, &fakeSigner{}, session())
	if _, err := exchange.Exchange(context.Background(), protocol.TransEmpCommand, []byte{0x22}); err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	// The exchange hands the connection the frame body; the transport's WriteFrame prepends the
	// one stream length on the wire. So what the connection sees opens with the service type
	// itself, which is 12 for a request, not a length.
	service := conn.sent[0]
	first := protocol.NewReader(service)
	if got, _ := first.U32(); got != 12 {
		t.Errorf("service type = %d, want 12", got)
	}
	if len(service) <= 16 {
		t.Errorf("the service request is only %d bytes", len(service))
	}
}

// A refusal is the service's answer, not a transport failure, and a caller has to be able
// to read what it said.
func TestExchangeSurfacesRefusals(t *testing.T) {
	conn := &fakeConn{replies: []protocol.SSOResponse{
		{Code: -10003, Message: "身份验证失败"},
	}}
	exchange := NewServiceExchange(conn, &fakeSigner{}, session())

	_, err := exchange.Exchange(context.Background(), protocol.TransEmpCommand, nil)
	var refusal *Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("error is %v, want *Refusal", err)
	}
	if refusal.Code != -10003 {
		t.Errorf("code = %d, want -10003", refusal.Code)
	}
	if refusal.Command != protocol.TransEmpCommand {
		t.Errorf("command = %q", refusal.Command)
	}
	if !bytes.Contains([]byte(refusal.Error()), []byte("身份验证失败")) {
		t.Errorf("the refusal lost its wording: %v", refusal)
	}
}

func TestExchangeSurfacesFailures(t *testing.T) {
	check := func(name string, conn Connection, signer Signer) {
		exchange := NewServiceExchange(conn, signer, session())
		if _, err := exchange.Exchange(context.Background(), protocol.TransEmpCommand, nil); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	check("a signer that fails", &fakeConn{}, &fakeSigner{err: errors.New("no signer")})
	check("a connection that fails",
		&fakeConn{err: errors.New("dropped")}, &fakeSigner{})
	check("nothing wired",
		nil, &fakeSigner{})
}
