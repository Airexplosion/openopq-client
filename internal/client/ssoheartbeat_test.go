package client

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"openopq/internal/protocol"
)

// beatExchange counts what the heartbeat sends. The count is guarded because the heartbeat
// runs on a goroutine of its own, and a test that read the slice while it was being appended
// to would be testing the scheduler.
type beatExchange struct {
	mu    sync.Mutex
	calls []string
}

type failingHeartbeatExchange struct {
	mu    sync.Mutex
	count int
	auth  bool
}

func (e *failingHeartbeatExchange) Exchange(context.Context, string, []byte) ([]byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.count++
	if e.auth {
		return nil, &Refusal{Code: -10003, Message: "authentication invalid"}
	}
	if e.count == 1 {
		return nil, errors.New("temporary signer outage")
	}
	return nil, nil
}
func (e *failingHeartbeatExchange) calls() int { e.mu.Lock(); defer e.mu.Unlock(); return e.count }
func TestHeartbeatSurvivesTransientSignerFailureButStopsOnAuthenticationRefusal(t *testing.T) {
	transient := &failingHeartbeatExchange{}
	beat := startSSOHeartbeat(transient, 2*time.Millisecond)
	deadline := time.Now().Add(time.Second)
	for transient.calls() < 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	beat.Close()
	if transient.calls() < 3 {
		t.Fatal("temporary signer error permanently disabled heartbeat")
	}
	auth := &failingHeartbeatExchange{auth: true}
	beat = startSSOHeartbeat(auth, 2*time.Millisecond)
	select {
	case <-beat.done:
	case <-time.After(time.Second):
		beat.Close()
		t.Fatal("invalid credentials still being retried")
	}
	if auth.calls() != 1 {
		t.Fatal("authentication refusal retried")
	}
	beat.Close()
}

func (b *beatExchange) Exchange(_ context.Context, command string, _ []byte) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls = append(b.calls, command)
	return protocol.Marshal(protocol.VarintField(1, 1)), nil
}

func (b *beatExchange) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.calls)
}

func (b *beatExchange) first() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.calls) == 0 {
		return ""
	}
	return b.calls[0]
}

// The protocol heartbeat goes on its own, at its own cadence, and it is a request like any
// other rather than something written to the connection.
func TestSSOHeartbeatReportsOnItsOwn(t *testing.T) {
	exchange := &beatExchange{}
	beat := startSSOHeartbeat(exchange, 2*time.Millisecond)
	defer beat.Close()

	deadline := time.Now().Add(5 * time.Second)
	for exchange.count() < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("the heartbeat reported %d times, want at least 2", exchange.count())
		}
		time.Sleep(time.Millisecond)
	}
	if got := exchange.first(); got != protocol.SSOHeartbeatCommand {
		t.Errorf("the heartbeat was sent to %q, want %q", got, protocol.SSOHeartbeatCommand)
	}
}

// Nothing to send through is nothing to report about, so there is no heartbeat rather than one
// that cannot be sent.
func TestSSOHeartbeatNeedsAnExchange(t *testing.T) {
	if startSSOHeartbeat(nil, time.Millisecond) != nil {
		t.Error("a heartbeat was started with nothing to send through")
	}
}

// A caller that has closed a bot has to know nothing is still writing to it, and closing twice
// has to be as harmless as it is everywhere else.
func TestSSOHeartbeatCloseIsIdempotent(t *testing.T) {
	beat := startSSOHeartbeat(&beatExchange{}, time.Hour)
	beat.Close()
	beat.Close()

	var none *ssoHeartbeat
	none.Close()
}
