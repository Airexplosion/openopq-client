package client

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"time"

	"openopq/internal/bot"
	"openopq/internal/protocol"
	"openopq/internal/transport"
)

// ReconnectOptions controls recovery of an already authenticated QQ connection.
// Zero values enable unlimited network retries with a 2s–60s exponential backoff.
// In-process recovery reuses tickets already in this Bot's memory. Process
// restart restore is handled by PersistLogin/ResumeLogin.
type ReconnectOptions struct {
	Disabled       bool
	MinDelay       time.Duration
	MaxDelay       time.Duration
	AttemptTimeout time.Duration
}

func (o ReconnectOptions) defaults() ReconnectOptions {
	if o.MinDelay <= 0 {
		o.MinDelay = 2 * time.Second
	}
	if o.MaxDelay <= 0 {
		o.MaxDelay = time.Minute
	}
	if o.MaxDelay < o.MinDelay {
		o.MaxDelay = o.MinDelay
	}
	if o.AttemptTimeout <= 0 {
		o.AttemptTimeout = 45 * time.Second
	}
	return o
}

func (o ReconnectOptions) delay(attempt int) time.Duration {
	d := o.MinDelay
	for i := 1; i < attempt && d < o.MaxDelay; i++ {
		if d > o.MaxDelay/2 {
			d = o.MaxDelay
			break
		}
		d *= 2
	}
	// Spread reconnects of multiple accounts; never exceed the configured ceiling.
	d = time.Duration(float64(d) * (0.8 + rand.Float64()*0.2))
	return d
}

type sessionTerminated struct{ reason string }

func (e *sessionTerminated) Error() string { return e.reason }

func exchangeAuthenticationError(exchange Exchange) error {
	if s, ok := exchange.(interface{ AuthenticationError() error }); ok {
		return s.AuthenticationError()
	}
	return nil
}

func isNetworkFailure(err error) bool {
	var n net.Error
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) ||
		errors.Is(err, transport.ErrClosed) || errors.Is(err, transport.ErrNoReply) || errors.As(err, &n)
}

func waitRecovery(ctx context.Context, delay time.Duration) bool {
	t := time.NewTimer(delay)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return ctx.Err() == nil
	}
}

func (b *Bot) ReconnectStatus() bot.ReconnectStatus {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.recovery
	if s.NextRetryAt != nil {
		copy := *s.NextRetryAt
		s.NextRetryAt = &copy
	}
	return s
}

func (b *Bot) reconnectLog(level slog.Level, msg string, args ...any) {
	logger := b.options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger.Log(context.Background(), level, msg, args...)
}

func (b *Bot) startConnectionLoop() {
	b.mu.Lock()
	if b.recoveryDone != nil || b.connection == nil || !b.online {
		b.mu.Unlock()
		return
	}
	if b.beat == nil {
		b.beat = startSSOHeartbeat(b.exchange, b.options.Heartbeat, b.heartbeatLogger())
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	b.recoveryCancel, b.recoveryDone = cancel, done
	conn, exchange, uin := b.connection, b.exchange, b.account.Uin
	b.mu.Unlock()
	go func() {
		defer close(done)
		defer func() {
			b.mu.Lock()
			if b.recoveryDone == done {
				b.recoveryCancel = nil
				b.recoveryDone = nil
			}
			b.mu.Unlock()
		}()
		b.connectionLoop(ctx, done, conn, exchange, uin)
	}()
}

// Exactly one supervisor owns this account's reader, retries and worker replacement.
// Subscribers and the signer lease outlive a temporary TCP outage.
func (b *Bot) connectionLoop(ctx context.Context, generation chan struct{}, conn Connection, exchange Exchange, uin uint32) {
	options := b.options.Reconnect.defaults()
	attempt := 0
	connectedAt := time.Now()
	for {
		err := b.readEvents(ctx, conn, exchange, uin)
		if ctx.Err() != nil {
			return
		}
		if auth := exchangeAuthenticationError(exchange); auth != nil {
			err = auth
		}
		b.mu.Lock()
		if b.recoveryDone != generation || b.recoveryCancel == nil || ctx.Err() != nil {
			b.mu.Unlock()
			return
		}
		keep, beat, stopO3, o3Done := b.keepAlive, b.beat, b.o3Cancel, b.o3Done
		if beat != nil {
			b.lastHeartbeat = beat.Snapshot()
		}
		b.online = false
		b.connection, b.exchange, b.login, b.keepAlive, b.beat, b.o3Cancel, b.o3Done = nil, nil, nil, nil, nil, nil, nil
		b.mu.Unlock()
		if stopO3 != nil {
			stopO3()
		}
		closeConnection(conn)
		keep.Close()
		beat.Close()
		if o3Done != nil {
			<-o3Done
		}
		if ctx.Err() != nil {
			return
		}
		var refusal *Refusal
		var ended *sessionTerminated
		if errors.As(err, &refusal) || errors.As(err, &ended) {
			b.stopRecovery(ctx, generation, err, true)
			return
		}
		if options.Disabled || b.options.Dial == nil || b.options.Signer == nil {
			b.stopRecovery(ctx, generation, err, false)
			return
		}
		if time.Since(connectedAt) >= time.Minute {
			attempt = 0
		}
		for {
			attempt++
			next := time.Now().Add(options.delay(attempt))
			b.mu.Lock()
			if ctx.Err() != nil || b.recoveryDone != generation || b.recoveryCancel == nil {
				b.mu.Unlock()
				return
			}
			b.recovery = bot.ReconnectStatus{Reconnecting: true, Attempt: attempt, NextRetryAt: &next, LastError: err.Error()}
			b.mu.Unlock()
			b.reconnectLog(slog.LevelWarn, "QQ reconnect scheduled", "account", uin, "attempt", attempt, "next_retry_at", next, "error", err)
			if !waitRecovery(ctx, time.Until(next)) {
				return
			}
			b.mu.Lock()
			b.recovery.NextRetryAt = nil
			b.mu.Unlock()
			attemptCtx, cancel := context.WithTimeout(ctx, options.AttemptTimeout)
			var fatal bool
			conn, exchange, err, fatal = b.reconnectAttempt(attemptCtx, generation, false)
			cancel()
			if ctx.Err() != nil {
				return
			}
			if fatal {
				b.stopRecovery(ctx, generation, err, true)
				return
			}
			if err != nil {
				continue
			}
			connectedAt = time.Now()
			b.reconnectLog(slog.LevelInfo, "QQ connection restored", "account", uin, "attempt", attempt)
			break
		}
	}
}

func closeConnection(conn Connection) {
	if closer, ok := conn.(interface{ Close() error }); ok {
		_ = closer.Close()
	}
}

func (b *Bot) stopRecovery(ctx context.Context, generation chan struct{}, err error, needsLogin bool) {
	b.mu.Lock()
	if ctx.Err() != nil || b.recoveryDone != generation || b.recoveryCancel == nil {
		b.mu.Unlock()
		return
	}
	b.recovery = bot.ReconnectStatus{LastError: err.Error(), RequiresLogin: needsLogin}
	if needsLogin {
		b.terminalErr = err
	}
	uin := b.account.Uin
	b.mu.Unlock()
	if needsLogin {
		b.invalidateSavedLogin()
	}
	b.reconnectLog(slog.LevelWarn, "QQ reconnect stopped", "account", uin, "requires_login", needsLogin, "error", err)
}

// In-process TCP recovery skips the login ECDH and reuses tickets already in
// memory. A cold start (resume from disk) performs that greeting first so the
// service sees the same connection setup a QR login uses, then installs the
// saved tickets and Register. Only the stored account's O3 work and Register
// are sent afterwards; no new device or business-message replay.
func (b *Bot) reconnectAttempt(ctx context.Context, generation chan struct{}, cold bool) (Connection, Exchange, error, bool) {
	b.mu.Lock()
	session := b.account
	b.mu.Unlock()
	if session.Uin == 0 || len(session.SessionKey) == 0 || len(session.D2) == 0 {
		return nil, nil, &sessionTerminated{reason: "登录票据不完整，需要重新扫码"}, true
	}
	if prepared, ok := b.options.Signer.(interface {
		Prepare(context.Context, []byte, uint32) error
	}); ok {
		if err := prepared.Prepare(ctx, b.options.Device.Guid, session.Uin); err != nil {
			return nil, nil, err, false
		}
	}
	conn, err := b.options.Dial(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("reconnect TCP: %w", err), false
	}
	committed := false
	defer func() {
		if !committed {
			closeConnection(conn)
		}
	}()
	// Closing a candidate not yet published to Bot must still interrupt a blocked write.
	closed := make(chan struct{})
	interrupt := context.AfterFunc(ctx, func() { closeConnection(conn); close(closed) })
	interruptHandled := false
	defer func() {
		if !interruptHandled && !interrupt() {
			<-closed
		}
	}()
	live := session
	if cold {
		live.Token, live.D2, live.SessionKey = nil, nil, nil
	}
	exchange := NewServiceExchange(conn, b.options.Signer, &live)
	if cold {
		if err := b.keyExchange(ctx, exchange); err != nil {
			b.reconnectLog(slog.LevelWarn, "saved QQ login key exchange failed; continuing with stored tickets", "account", session.Uin, "error", err)
		}
		live.Token, live.D2, live.SessionKey = session.Token, session.D2, session.SessionKey
	}
	if setter, ok := conn.(interface{ SetSessionKey([]byte) }); ok {
		setter.SetSessionKey(live.SessionKey)
	}
	keep, err := startKeepAlive(conn, b.options.Token, b.options.KeepAlive)
	if err != nil {
		return nil, nil, err, false
	}
	defer func() {
		if !committed {
			closeConnection(conn)
			keep.Close()
		}
	}()
	if account, ok := b.options.Signer.(interface {
		Account(context.Context, uint32, bool) error
	}); ok {
		if err := account.Account(ctx, live.Uin, true); err != nil {
			return nil, nil, err, false
		}
	}
	// A restarted signer may be waiting for a fresh secure response before it
	// signs Register. Keep O3 moving concurrently; join this relay before handing
	// the socket to the ordinary background worker (one consumer per account).
	relayCtx, stopRelay := context.WithCancel(ctx)
	relayDone := make(chan struct{})
	_, token := signerSession(b.options.Signer)
	if token != "" && !b.options.DisableO3Relay {
		go func() {
			defer close(relayDone)
			for relayCtx.Err() == nil {
				b.o3DelegateWithTrigger(relayCtx, exchange, 6*time.Second, false, false)
				if !waitRecovery(relayCtx, 500*time.Millisecond) {
					return
				}
			}
		}()
	} else {
		close(relayDone)
	}
	defer func() { stopRelay(); <-relayDone }()
	input := b.options.Register
	input.GUID = hex.EncodeToString(b.options.Device.Guid)
	input.ServiceID = live.MsfID
	answer, err := exchange.Exchange(ctx, protocol.RegisterCommand, protocol.BuildRegister(input))
	if err != nil {
		if auth := exchangeAuthenticationError(exchange); auth != nil {
			return nil, nil, auth, true
		}
		var refusal *Refusal
		if errors.As(err, &refusal) {
			return nil, nil, err, !cold || refusal.Code == -10003
		}
		return nil, nil, err, false
	}
	if err = protocol.ParseRegister(answer); err != nil {
		return nil, nil, err, !cold
	}
	stopRelay()
	<-relayDone
	if err = exchangeAuthenticationError(exchange); err != nil {
		return nil, nil, err, true
	}
	// Stop the attempt's close callback BEFORE publishing the live connection.
	stopped := interrupt()
	interruptHandled = true
	if !stopped {
		<-closed
		return nil, nil, ctx.Err(), false
	}
	if ctx.Err() != nil {
		return nil, nil, ctx.Err(), false
	}
	b.mu.Lock()
	if ctx.Err() != nil || b.recoveryDone != generation || b.recoveryCancel == nil {
		b.mu.Unlock()
		return nil, nil, context.Canceled, false
	}
	b.connection, b.exchange, b.keepAlive = conn, exchange, keep
	b.beat = startSSOHeartbeat(exchange, b.options.Heartbeat, b.heartbeatLogger())
	b.online = true
	b.terminalErr = nil
	b.recovery = bot.ReconnectStatus{}
	committed = true
	b.mu.Unlock()
	b.startO3Relay(exchange)
	return conn, exchange, nil, false
}
