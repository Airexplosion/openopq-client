package client

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"openopq/internal/protocol"
	"openopq/internal/sign"
)

func TestALostSignerSessionMakesTheConnectionRebuild(t *testing.T) {
	signer := &fakeSigner{err: fmt.Errorf("signer returned HTTP 409 (prepare_signer_session_first): %w", sign.ErrSessionLost)}
	exchange := NewServiceExchange(&fakeConn{}, signer, session())
	if exchangeSignerSessionError(exchange) != nil {
		t.Fatal("a fresh exchange reported a lost signer session")
	}
	if _, err := exchange.Exchange(context.Background(), protocol.TransEmpCommand, nil); err == nil {
		t.Fatal("a refused signature was accepted")
	}
	err := exchangeSignerSessionError(exchange)
	if !errors.Is(err, sign.ErrSessionLost) {
		t.Fatalf("lost signer session not reported: %v", err)
	}
	// It must be reconnectable, not treated as a refused login.
	var refusal *Refusal
	var ended *sessionTerminated
	if errors.As(err, &refusal) || errors.As(err, &ended) {
		t.Fatalf("lost signer session would stop recovery: %v", err)
	}
}

func TestOtherSignerFailuresDoNotRebuildTheConnection(t *testing.T) {
	exchange := NewServiceExchange(&fakeConn{}, &fakeSigner{err: errors.New("signer returned HTTP 502")}, session())
	_, _ = exchange.Exchange(context.Background(), protocol.TransEmpCommand, nil)
	if err := exchangeSignerSessionError(exchange); err != nil {
		t.Fatalf("a transient signer failure forced a reconnect: %v", err)
	}
}
