package client

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"openopq/internal/protocol"
)

type observedHeartbeatWire struct {
	*fakeConn
	writeError error
	replyError error
	code       int32
}

func (w *observedHeartbeatWire) ExchangeObserved(_ context.Context, _ []byte, _ uint32, sent func()) (protocol.SSOResponse, error) {
	if w.writeError != nil {
		return protocol.SSOResponse{}, w.writeError
	}
	sent()
	return protocol.SSOResponse{Code: w.code}, w.replyError
}

func TestHeartbeatDistinguishesSignWriteReplyAndAcceptance(t *testing.T) {
	for _, mode := range []string{"sign failure", "write failure", "reply timeout", "refused", "success"} {
		t.Run(mode, func(t *testing.T) {
			wire := &observedHeartbeatWire{fakeConn: &fakeConn{}}
			signer := &fakeSigner{}
			switch mode {
			case "sign failure":
				signer.err = errors.New("signer offline")
			case "write failure":
				wire.writeError = io.ErrClosedPipe
			case "reply timeout":
				wire.replyError = context.DeadlineExceeded
			case "refused":
				wire.code = -10003
			}
			session := session()
			session.SessionKey = bytes.Repeat([]byte{1}, 16)
			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, nil)).With("account", 123456)
			h := startSSOHeartbeat(NewServiceExchange(wire, signer, session), 2*time.Millisecond, logger)
			awaitRecovery(t, func() bool {
				status := h.Snapshot()
				return status.LastSuccessAt != nil || status.ConsecutiveFailures > 0
			})
			h.Close()
			status := h.Snapshot()
			if mode == "success" {
				if status.LastSignedAt == nil || status.LastSentAt == nil || status.LastReplyAt == nil || status.LastSuccessAt == nil || *status.LastReplyCode != 0 {
					t.Fatalf("incomplete success evidence: %+v", status)
				}
				for _, stage := range []string{"signing", "signed", "sending", "sent", "reply"} {
					if !strings.Contains(logs.String(), `"stage":"`+stage+`"`) {
						t.Fatalf("missing %s log", stage)
					}
				}
				return
			}
			if status.LastSuccessAt != nil {
				t.Fatal("failed request counted as confirmed")
			}
			wantStage := map[string]string{"sign failure": "signing", "write failure": "sending", "reply timeout": "sent", "refused": "reply"}[mode]
			if status.LastFailureStage != wantStage {
				t.Fatalf("failure stage=%s, want %s", status.LastFailureStage, wantStage)
			}
			if mode == "sign failure" && status.LastSignedAt != nil {
				t.Fatal("failed signature called signed")
			}
			if mode == "write failure" && status.LastSentAt != nil {
				t.Fatal("failed write called sent")
			}
			if mode == "reply timeout" && status.LastReplyAt != nil {
				t.Fatal("timeout called server reply")
			}
			if mode == "refused" && (!status.Stale || *status.LastReplyCode != -10003) {
				t.Fatal("lost authentication refusal")
			}
		})
	}
}

type stalledHeartbeat struct{ entered chan struct{} }

func (e *stalledHeartbeat) Exchange(ctx context.Context, _ string, _ []byte) ([]byte, error) {
	close(e.entered)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestHeartbeatWatchdogDetectsStalledWorkerAndCloseCancelsIt(t *testing.T) {
	exchange := &stalledHeartbeat{entered: make(chan struct{})}
	h := startSSOHeartbeat(exchange, time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer h.Close()
	<-exchange.entered
	h.mu.Lock()
	h.status.ConnectionSince = time.Now().Add(-time.Hour)
	h.mu.Unlock()
	var expired *heartbeatExpired
	if !errors.As(h.HealthError(), &expired) {
		t.Fatal("stalled worker remained healthy")
	}
	done := make(chan struct{})
	go func() { h.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cannot cancel stalled heartbeat")
	}
}

func TestHeartbeatUsesSuccessNotSigningOrTrafficAsDeadlineBaseline(t *testing.T) {
	now := time.Now()
	old := now.Add(-time.Hour)
	h := &ssoHeartbeat{interval: protocol.SSOHeartbeatInterval, timeout: ssoHeartbeatTimeout}
	h.status.ConnectionSince = old
	h.status.LastSignedAt = &now
	h.status.LastSentAt = &now
	h.status.LastReplyAt = &now
	if h.HealthError() == nil {
		t.Fatal("sign/write/refused reply postponed heartbeat deadline")
	}
	h.fault = nil
	h.status.LastSuccessAt = &now
	if h.HealthError() != nil {
		t.Fatal("confirmed recent heartbeat treated as stale")
	}
	h.status.ConsecutiveFailures = 1
	if h.HealthError() != nil {
		t.Fatal("one transient failure forced reconnect")
	}
	h.status.ConsecutiveFailures = 2
	if h.HealthError() == nil {
		t.Fatal("consecutive failures did not force reconnect")
	}
}

func TestHeartbeatSnapshotDoesNotExposeMutablePointers(t *testing.T) {
	h := startSSOHeartbeat(&beatExchange{}, time.Hour)
	defer h.Close()
	now := time.Now()
	code := int32(0)
	h.mu.Lock()
	h.status.LastSuccessAt = &now
	h.status.LastReplyCode = &code
	h.mu.Unlock()
	s := h.Snapshot()
	*s.LastSuccessAt = time.Time{}
	*s.LastReplyCode = -10003
	actual := h.Snapshot()
	if actual.LastSuccessAt.IsZero() || *actual.LastReplyCode != 0 {
		t.Fatal("snapshot mutated worker state")
	}
}
