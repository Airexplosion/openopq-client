package client

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"openopq/internal/bot"
	"openopq/internal/protocol"
)

const ssoHeartbeatTimeout = 30 * time.Second

type heartbeatTraceKey struct{}
type heartbeatTrace struct {
	stage    string
	sequence uint32
	code     *int32
	err      error
}

func traceHeartbeat(ctx context.Context, event heartbeatTrace) {
	if observer, ok := ctx.Value(heartbeatTraceKey{}).(func(heartbeatTrace)); ok {
		observer(event)
	}
}

// The reader supervises this worker independently: a stalled worker must not
// keep a live TCP connection falsely online forever.
type ssoHeartbeat struct {
	exchange  Exchange
	interval  time.Duration
	timeout   time.Duration
	stop      chan struct{}
	done      chan struct{}
	closeOnce sync.Once
	ctx       context.Context
	cancel    context.CancelFunc
	logger    *slog.Logger
	mu        sync.Mutex
	status    bot.HeartbeatStatus
	terminal  error
	fault     error
}

func startSSOHeartbeat(exchange Exchange, interval time.Duration, loggers ...*slog.Logger) *ssoHeartbeat {
	if exchange == nil {
		return nil
	}
	if interval <= 0 {
		interval = protocol.SSOHeartbeatInterval
	}
	logger := slog.Default()
	if len(loggers) > 0 && loggers[0] != nil {
		logger = loggers[0]
	}
	h := &ssoHeartbeat{exchange: exchange, interval: interval, timeout: ssoHeartbeatTimeout,
		stop: make(chan struct{}), done: make(chan struct{}), logger: logger,
		status: bot.HeartbeatStatus{ConnectionSince: time.Now(), Stage: "waiting"}}
	h.ctx, h.cancel = context.WithCancel(context.Background())
	go h.run()
	return h
}

func (h *ssoHeartbeat) observe(event heartbeatTrace) {
	if h.ctx.Err() != nil {
		return
	}
	now := time.Now()
	h.mu.Lock()
	h.status.Stage = event.stage
	if event.stage == "signed" {
		h.status.LastSignedAt = &now
	}
	if event.stage == "sent" {
		h.status.LastSentAt = &now
	}
	if event.code != nil {
		code := *event.code
		h.status.LastReplyAt = &now
		h.status.LastReplyCode = &code
	}
	if event.err != nil {
		h.status.LastFailureStage = event.stage
	}
	h.mu.Unlock()
	level := slog.LevelInfo
	if event.err != nil {
		level = slog.LevelWarn
	}
	args := []any{"stage", event.stage, "sequence", event.sequence}
	if event.code != nil {
		args = append(args, "code", *event.code)
	}
	if event.err != nil {
		args = append(args, "error", event.err)
	}
	h.logger.Log(h.ctx, level, "QQ SSO heartbeat stage", args...)
}

func (h *ssoHeartbeat) run() {
	defer close(h.done)
	ticker := time.NewTicker(h.interval)
	defer ticker.Stop()
	for {
		select {
		case <-h.stop:
			return
		case <-ticker.C:
		}
		if h.ctx.Err() != nil {
			return
		}
		now := time.Now()
		h.mu.Lock()
		h.status.LastAttemptAt = &now
		h.status.Stage = "started"
		h.mu.Unlock()
		h.logger.Info("QQ SSO heartbeat started")
		ctx, cancel := context.WithTimeout(h.ctx, h.timeout)
		ctx = context.WithValue(ctx, heartbeatTraceKey{}, func(event heartbeatTrace) { h.observe(event) })
		_, err := h.exchange.Exchange(ctx, protocol.SSOHeartbeatCommand, protocol.BuildSSOHeartbeat())
		cancel()
		if h.ctx.Err() != nil {
			return
		}
		h.mu.Lock()
		if err == nil {
			now := time.Now()
			h.status.LastSuccessAt = &now
			h.status.ConsecutiveFailures = 0
			h.status.Stage = "success"
			h.status.LastError = ""
			h.status.LastFailureStage = ""
		} else {
			h.status.ConsecutiveFailures++
			h.status.LastError = err.Error()
			h.status.LastFailureStage = h.status.Stage
			h.status.Stage = "failed"
			var refusal *Refusal
			if errors.As(err, &refusal) && refusal.Code == -10003 {
				h.terminal = err
			}
			if h.status.ConsecutiveFailures >= 2 {
				h.fault = &heartbeatExpired{stage: h.status.LastFailureStage, failures: h.status.ConsecutiveFailures}
			}
		}
		failures, terminal, fault := h.status.ConsecutiveFailures, h.terminal, h.fault
		h.mu.Unlock()
		if err == nil {
			h.logger.Info("QQ SSO heartbeat confirmed", "elapsed_ms", time.Since(now).Milliseconds())
		} else {
			h.logger.Warn("QQ SSO heartbeat failed", "error", err, "consecutive_failures", failures, "elapsed_ms", time.Since(now).Milliseconds())
		}
		if terminal != nil || fault != nil {
			return
		}
	}
}

type heartbeatExpired struct {
	stage    string
	failures int
}

func (e *heartbeatExpired) Error() string {
	return fmt.Sprintf("QQ SSO heartbeat unconfirmed: stage=%s consecutive_failures=%d", e.stage, e.failures)
}

func (h *ssoHeartbeat) healthErrorLocked(now time.Time) error {
	if h.terminal != nil {
		return h.terminal
	}
	if h.fault != nil {
		return h.fault
	}
	baseline := h.status.ConnectionSince
	if h.status.LastSuccessAt != nil {
		baseline = *h.status.LastSuccessAt
	}
	// Two missed cycles plus one request budget. This also detects a stalled
	// worker independently of its own timer, while tolerating one brief outage.
	if h.status.ConsecutiveFailures >= 2 || now.Sub(baseline) > 2*h.interval+h.timeout {
		h.fault = &heartbeatExpired{stage: h.status.Stage, failures: h.status.ConsecutiveFailures}
		return h.fault
	}
	return nil
}

func (h *ssoHeartbeat) HealthError() error {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.healthErrorLocked(time.Now())
}
func (h *ssoHeartbeat) Snapshot() bot.HeartbeatStatus {
	if h == nil {
		return bot.HeartbeatStatus{}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.status
	s.Stale = h.healthErrorLocked(time.Now()) != nil
	return cloneHeartbeatStatus(s)
}

func cloneHeartbeatStatus(s bot.HeartbeatStatus) bot.HeartbeatStatus {
	for _, field := range []**time.Time{&s.LastAttemptAt, &s.LastSignedAt, &s.LastSentAt, &s.LastReplyAt, &s.LastSuccessAt} {
		if *field != nil {
			copy := **field
			*field = &copy
		}
	}
	if s.LastReplyCode != nil {
		copy := *s.LastReplyCode
		s.LastReplyCode = &copy
	}
	return s
}

// Called while Bot.mu is held at initial register and connection replacement.
func (b *Bot) heartbeatLogger() *slog.Logger {
	logger := b.options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return logger.With("account", b.account.Uin)
}

func (b *Bot) HeartbeatStatus() bot.HeartbeatStatus {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.beat != nil {
		return b.beat.Snapshot()
	}
	return cloneHeartbeatStatus(b.lastHeartbeat)
}

func (h *ssoHeartbeat) Close() {
	if h == nil {
		return
	}
	h.closeOnce.Do(func() { h.cancel(); close(h.stop) })
	<-h.done
}
