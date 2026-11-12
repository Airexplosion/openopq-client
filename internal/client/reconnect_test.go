package client

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"openopq/internal/bot"
	"openopq/internal/protocol"
	"openopq/internal/sign"
)

type recoverySigner struct {
	mu           sync.Mutex
	calls        map[uint32]string
	preparedGUID []byte
	preparedUin  uint32
	accountUin   uint32
	released     atomic.Int32
	secureReady  chan struct{}
	pulled       atomic.Bool
	acknowledged atomic.Bool
}

func (s *recoverySigner) Sign(ctx context.Context, cmd string, seq uint32, _ []byte) (sign.Bundle, error) {
	if cmd == protocol.RegisterCommand && s.secureReady != nil {
		select {
		case <-ctx.Done():
			return sign.Bundle{}, ctx.Err()
		case <-s.secureReady:
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.calls == nil {
		s.calls = make(map[uint32]string)
	}
	s.calls[seq] = cmd
	return sign.Bundle{Sign: []byte{1}}, nil
}
func (s *recoverySigner) Prepare(_ context.Context, guid []byte, uin uint32) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.preparedGUID = append([]byte(nil), guid...)
	s.preparedUin = uin
	return nil
}
func (s *recoverySigner) Account(_ context.Context, uin uint32, logged bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if logged {
		s.accountUin = uin
	}
	return nil
}
func (s *recoverySigner) Release() error { s.released.Add(1); return nil }

func (s *recoverySigner) O3Session() (string, string) {
	if s.secureReady != nil {
		return "http://signer.invalid", "test-owner"
	}
	return "", ""
}
func (s *recoverySigner) DoO3(_ context.Context, method, path string, body []byte) (*http.Response, error) {
	value := `{"empty":true}`
	if method == http.MethodGet && path == "/o3/pull" && s.pulled.CompareAndSwap(false, true) {
		value = `{"subseq":"17","cmd":"trpc.o3.ecdh_access.EcdhAccess.SsoSecureA2Establish","body":"0801"}`
	}
	if method == http.MethodPost && path == "/o3/recv" {
		if !bytes.Contains(body, []byte(`"subseq":17`)) && !bytes.Contains(body, []byte(`"subseq":"17"`)) {
			return nil, errors.New("wrong O3 callback owner")
		}
		if s.acknowledged.CompareAndSwap(false, true) {
			close(s.secureReady)
		}
		value = `{"ok":true}`
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(value))}, nil
}

type recoveryWire struct {
	signer        *recoverySigner
	done          chan struct{}
	once          sync.Once
	push          chan protocol.SSOResponse
	mu            sync.Mutex
	key           []byte
	commands      []string
	packets       [][]byte
	code          int32
	heartbeatErr  error
	heartbeatCode int32
	o3Code        int32
	blockRegister bool
	entered       chan struct{}
}

func newRecoveryWire(s *recoverySigner) *recoveryWire {
	return &recoveryWire{signer: s, done: make(chan struct{}), push: make(chan protocol.SSOResponse, 4), entered: make(chan struct{}, 1)}
}
func (w *recoveryWire) Send([]byte) error {
	if w.Closed() {
		return io.EOF
	}
	return nil
}
func (w *recoveryWire) Close() error { w.once.Do(func() { close(w.done) }); return nil }
func (w *recoveryWire) Closed() bool {
	select {
	case <-w.done:
		return true
	default:
		return false
	}
}
func (w *recoveryWire) SetSessionKey(key []byte) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.key = append([]byte(nil), key...)
}
func (w *recoveryWire) Receive(ctx context.Context) (protocol.SSOResponse, error) {
	select {
	case <-ctx.Done():
		return protocol.SSOResponse{}, ctx.Err()
	case <-w.done:
		return protocol.SSOResponse{}, io.EOF
	case p := <-w.push:
		return p, nil
	}
}
func (w *recoveryWire) Exchange(ctx context.Context, p []byte, seq uint32) (protocol.SSOResponse, error) {
	w.signer.mu.Lock()
	cmd := w.signer.calls[seq]
	w.signer.mu.Unlock()
	w.mu.Lock()
	w.commands = append(w.commands, cmd)
	w.packets = append(w.packets, append([]byte(nil), p...))
	w.mu.Unlock()
	if cmd == protocol.KeyExchangeCommand {
		return protocol.SSOResponse{Code: 0, Body: []byte{0}}, nil
	}
	if cmd == protocol.RegisterCommand {
		select {
		case w.entered <- struct{}{}:
		default:
		}
		if w.blockRegister {
			select {
			case <-ctx.Done():
				return protocol.SSOResponse{}, ctx.Err()
			case <-w.done:
				return protocol.SSOResponse{}, io.EOF
			}
		}
		return protocol.SSOResponse{Code: w.code, Body: registerReply(), Message: "test refusal"}, nil
	}
	if cmd == protocol.SSOHeartbeatCommand {
		return protocol.SSOResponse{Code: w.heartbeatCode}, w.heartbeatErr
	}
	if o3SecureAccessCommand(cmd) {
		return protocol.SSOResponse{Code: w.o3Code, Body: protocol.LV32([]byte{8, 1, 2})}, nil
	}
	// A request whose outcome is uncertain must never be replayed by recovery.
	_ = w.Close()
	return protocol.SSOResponse{}, io.EOF
}

func TestReconnectRecoversSilentLiveSocketAndSupervisesMissingHeartbeat(t *testing.T) {
	for _, mode := range []string{"lost replies", "never scheduled"} {
		t.Run(mode, func(t *testing.T) {
			var next *recoveryWire
			b, old, s := recoveryBot(t, func(context.Context) (Connection, error) { return next, nil })
			next = newRecoveryWire(s)
			old.heartbeatErr = context.DeadlineExceeded
			interval := 5 * time.Millisecond
			if mode == "never scheduled" {
				interval = time.Hour
			}
			h := startSSOHeartbeat(b.exchange, interval, b.options.Logger)
			b.beat = h
			if mode == "lost replies" {
				select {
				case <-h.done:
				case <-time.After(time.Second):
					t.Fatal("missing failure threshold")
				}
			} else {
				h.mu.Lock()
				h.status.ConnectionSince = time.Now().Add(-3 * time.Hour)
				h.mu.Unlock()
			}
			if old.Closed() {
				t.Fatal("test requires an intact TCP connection")
			}
			self, err := b.Self(context.Background())
			if err != nil || self.Online {
				t.Fatal("stale session still advertised online")
			}
			b.startConnectionLoop()
			awaitRecovery(t, func() bool { b.mu.Lock(); defer b.mu.Unlock(); return b.online && b.connection == next })
			if !old.Closed() {
				t.Fatal("unhealthy connection not retired")
			}
			if s.released.Load() != 0 {
				t.Fatal("recovery lost signer lease")
			}
			next.push <- protocol.SSOResponse{Command: protocol.PushMessageCommand, Body: protocol.Marshal(protocol.BytesField(1, nil))}
		})
	}
}

func TestHeartbeatAuthRefusalStopsWithoutDialingAgain(t *testing.T) {
	var attempts atomic.Int32
	b, old, _ := recoveryBot(t, func(context.Context) (Connection, error) { attempts.Add(1); return nil, io.EOF })
	old.heartbeatCode = -10003
	h := startSSOHeartbeat(b.exchange, 2*time.Millisecond, b.options.Logger)
	b.beat = h
	select {
	case <-h.done:
	case <-time.After(time.Second):
		t.Fatal("heartbeat ignored refusal")
	}
	b.startConnectionLoop()
	awaitRecovery(t, func() bool { return b.ReconnectStatus().RequiresLogin })
	if attempts.Load() != 0 {
		t.Fatal("invalid credentials triggered network retries")
	}
	if !old.Closed() {
		t.Fatal("invalid connection left open")
	}
}

func recoveryBot(t *testing.T, dial Dialer) (*Bot, *recoveryWire, *recoverySigner) {
	t.Helper()
	s := &recoverySigner{}
	b := New(BotOptions{Dial: dial, Signer: s, Device: referenceDevice(), Session: Session{
		Uin: 123456, UID: "u_test", AppID: 537391652, MsfID: 2052, Framing: protocol.FramingSeven,
		SessionKey: bytes.Repeat([]byte{7}, 16), D2: []byte{2, 3}, Token: []byte{4, 5},
	}, Reconnect: ReconnectOptions{MinDelay: 5 * time.Millisecond, MaxDelay: 20 * time.Millisecond, AttemptTimeout: time.Second},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	w := newRecoveryWire(s)
	b.connection = w
	b.exchange = NewServiceExchange(w, s, &b.account)
	b.online = true
	b.credentials = protocol.Credentials{SessionKey: b.account.SessionKey}
	t.Cleanup(func() { _ = b.Close() })
	return b, w, s
}
func awaitRecovery(t *testing.T, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("recovery condition timed out")
}

func TestReconnectRetriesRegistersSameIdentityAndPreservesSubscription(t *testing.T) {
	var calls atomic.Int32
	var replacement *recoveryWire
	b, old, s := recoveryBot(t, func(ctx context.Context) (Connection, error) {
		if calls.Add(1) <= 2 {
			return nil, io.EOF
		}
		return replacement, nil
	})
	replacement = newRecoveryWire(s)
	b.options.Heartbeat = 10 * time.Millisecond
	original := b.Session()
	events, cancel := b.Subscribe()
	defer cancel()
	b.startConnectionLoop()
	// Exercise an actual failed business request, not just a manually set flag.
	if _, err := b.exchange.Exchange(context.Background(), "MessageSvc.PbSendMsg", []byte("uncertain")); err == nil {
		t.Fatal("missing send failure")
	}
	awaitRecovery(t, func() bool { b.mu.Lock(); defer b.mu.Unlock(); return b.online && b.connection == replacement })
	if calls.Load() != 3 {
		t.Fatalf("dial attempts=%d", calls.Load())
	}
	if s.released.Load() != 0 {
		t.Fatal("temporary outage released signer")
	}
	s.mu.Lock()
	if s.preparedUin != original.Uin || s.accountUin != original.Uin || !bytes.Equal(s.preparedGUID, b.options.Device.Guid) {
		t.Error("signer identity changed")
	}
	s.mu.Unlock()
	if !old.Closed() {
		t.Fatal("old socket left open")
	}
	replacement.mu.Lock()
	if !bytes.Equal(replacement.key, original.SessionKey) {
		t.Error("decoder session key not restored")
	}
	p := append([]byte(nil), replacement.packets[0]...)
	if replacement.commands[0] != protocol.RegisterCommand {
		t.Error("first signed command was not Register")
	}
	replacement.mu.Unlock()
	// Inspect the actual replacement wire: 7.x account header and encrypted body.
	r := protocol.NewReader(p)
	_, _ = r.U32()
	_, _ = r.U8()
	_, _ = r.LV32()
	uin, _ := r.U32()
	_, _ = r.U8()
	if uin != original.Uin {
		t.Fatalf("wire account=%d", uin)
	}
	plain, err := protocol.Decrypt(p[r.Pos():], original.SessionKey)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(plain, b.options.Device.Guid) || !bytes.Contains(plain, original.Token) {
		t.Fatal("GUID or login ticket lost from wire")
	}
	if got := b.Session(); !bytes.Equal(got.D2, original.D2) || got.Device != original.Device || got.UID != original.UID {
		t.Fatal("session replaced")
	}
	// A real decoded push must reach the SAME subscriber after reconnect.
	b.remember(987654, "u_peer", "Peer")
	body := protocol.Marshal(protocol.BytesField(1, protocol.Marshal(protocol.BytesField(1, protocol.Marshal(protocol.VarintField(1, 987654))))))
	replacement.push <- protocol.SSOResponse{Command: protocol.PushMessageCommand, Body: body}
	select {
	case e, ok := <-events:
		if !ok || e.SelfUin != original.Uin || e.Msg == nil || e.Msg.SenderUin != 987654 {
			t.Fatalf("bad push: %+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("subscriber lost")
	}
	awaitRecovery(t, func() bool {
		replacement.mu.Lock()
		defer replacement.mu.Unlock()
		return len(replacement.commands) > 1
	})
	replacement.mu.Lock()
	defer replacement.mu.Unlock()
	for _, cmd := range replacement.commands {
		if cmd != protocol.RegisterCommand && cmd != protocol.SSOHeartbeatCommand {
			t.Fatalf("replayed business/login command: %s", cmd)
		}
	}
}

func TestReconnectStopsForKickAuthenticationAndRegisterRefusal(t *testing.T) {
	for _, mode := range []string{"kick", "auth", "register"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			var next *recoveryWire
			b, old, s := recoveryBot(t, func(context.Context) (Connection, error) { calls.Add(1); return next, nil })
			next = newRecoveryWire(s)
			next.code = -10003
			b.startConnectionLoop()
			switch mode {
			case "kick":
				old.push <- protocol.SSOResponse{Command: "trpc.qq_new_tech.status_svc.StatusService.KickNT"}
			case "auth":
				old.push <- protocol.SSOResponse{Code: -10003, Command: "test"}
			case "register":
				_ = old.Close()
			}
			awaitRecovery(t, func() bool { return b.ReconnectStatus().RequiresLogin })
			b.mu.Lock()
			done := b.recoveryDone
			b.mu.Unlock()
			if done != nil {
				<-done
			}
			want := int32(0)
			if mode == "register" {
				want = 1
			}
			if calls.Load() != want {
				t.Fatalf("retried refusal: %d", calls.Load())
			}
			if b.AuthenticationError() == nil {
				t.Fatal("missing terminal error")
			}
			if _, err := b.Groups(context.Background()); !errors.Is(err, bot.ErrNotConnected) {
				t.Fatalf("business API still usable: %v", err)
			}
		})
	}
}

func TestCloseCancelsReconnectAtEveryStage(t *testing.T) {
	for _, mode := range []string{"backoff", "dial", "register"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			var next *recoveryWire
			entered := make(chan struct{})
			b, old, s := recoveryBot(t, func(ctx context.Context) (Connection, error) {
				calls.Add(1)
				if mode == "dial" {
					close(entered)
					<-ctx.Done()
					return nil, ctx.Err()
				}
				return next, nil
			})
			next = newRecoveryWire(s)
			next.blockRegister = true
			if mode == "backoff" {
				b.options.Reconnect.MinDelay = time.Hour
				b.options.Reconnect.MaxDelay = time.Hour
			}
			b.startConnectionLoop()
			_ = old.Close()
			switch mode {
			case "backoff":
				awaitRecovery(t, func() bool { return b.ReconnectStatus().Reconnecting })
			case "dial":
				<-entered
			case "register":
				<-next.entered
			}
			closed := make(chan struct{})
			go func() { _ = b.Close(); close(closed) }()
			select {
			case <-closed:
			case <-time.After(time.Second):
				t.Fatal("Close stuck")
			}
			if b.ReconnectStatus().Reconnecting {
				t.Fatal("retry survived Close")
			}
			b.mu.Lock()
			if b.online || b.connection != nil {
				t.Error("session resurrected")
			}
			b.mu.Unlock()
			if mode == "backoff" && calls.Load() != 0 {
				t.Fatal("dial after cancel")
			}
			if mode == "register" && !next.Closed() {
				t.Fatal("candidate socket leaked")
			}
		})
	}
}

func TestReconnectDisabledAndMissingTicketsDoNotLoop(t *testing.T) {
	for _, disabled := range []bool{true, false} {
		t.Run(map[bool]string{true: "disabled", false: "missing tickets"}[disabled], func(t *testing.T) {
			var calls atomic.Int32
			b, old, _ := recoveryBot(t, func(context.Context) (Connection, error) { calls.Add(1); return nil, io.EOF })
			b.options.Reconnect.Disabled = disabled
			if !disabled {
				b.account.SessionKey = nil
			}
			b.startConnectionLoop()
			b.mu.Lock()
			done := b.recoveryDone
			b.mu.Unlock()
			_ = old.Close()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("supervisor did not stop")
			}
			if calls.Load() != 0 {
				t.Fatal("unexpected dial")
			}
			if !disabled && !b.ReconnectStatus().RequiresLogin {
				t.Fatal("missing login notice")
			}
		})
	}
}

func TestReconnectRelaysO3BeforeSignerCanSignRegister(t *testing.T) {
	var next *recoveryWire
	b, old, s := recoveryBot(t, func(context.Context) (Connection, error) { return next, nil })
	s.secureReady = make(chan struct{})
	next = newRecoveryWire(s)
	b.startConnectionLoop()
	_ = old.Close()
	awaitRecovery(t, func() bool { b.mu.Lock(); defer b.mu.Unlock(); return b.online && b.connection == next })
	if !s.acknowledged.Load() {
		t.Fatal("Register completed without restoring O3")
	}
	next.mu.Lock()
	defer next.mu.Unlock()
	if len(next.commands) < 2 || !o3SecureAccessCommand(next.commands[0]) || next.commands[1] != protocol.RegisterCommand {
		t.Fatalf("wrong recovery order: %v", next.commands)
	}
}

func TestReconnectOutageDoesNotDisconnectOtherAccount(t *testing.T) {
	first, old, _ := recoveryBot(t, func(context.Context) (Connection, error) { return nil, io.EOF })
	second, live, _ := recoveryBot(t, func(context.Context) (Connection, error) { t.Error("unaffected account dialed"); return nil, io.EOF })
	first.options.Reconnect.MinDelay = time.Hour
	first.options.Reconnect.MaxDelay = time.Hour
	second.account.Uin = 234567
	second.options.Device.Guid = bytes.Repeat([]byte{9}, 16)
	second.account.Device = "09090909090909090909090909090909"
	first.startConnectionLoop()
	second.startConnectionLoop()
	events, cancel := second.Subscribe()
	defer cancel()
	_ = old.Close()
	awaitRecovery(t, func() bool { return first.ReconnectStatus().Reconnecting })
	live.push <- protocol.SSOResponse{Command: protocol.PushMessageCommand, Body: protocol.Marshal(protocol.BytesField(1, protocol.Marshal(protocol.BytesField(1, nil))))}
	select {
	case e := <-events:
		if e.SelfUin != 234567 {
			t.Fatalf("wrong account received push: %d", e.SelfUin)
		}
	case <-time.After(time.Second):
		t.Fatal("unaffected account lost events")
	}
	self, err := second.Self(context.Background())
	if err != nil || !self.Online || second.ReconnectStatus().Reconnecting || live.Closed() {
		t.Fatalf("unaffected account offline: %+v, %v", self, err)
	}
}

func TestReconnectStopsForO3AuthFailureWhileRegisterWaits(t *testing.T) {
	var next *recoveryWire
	var attempts atomic.Int32
	b, old, s := recoveryBot(t, func(context.Context) (Connection, error) { attempts.Add(1); return next, nil })
	s.secureReady = make(chan struct{})
	b.options.Reconnect.AttemptTimeout = 50 * time.Millisecond
	next = newRecoveryWire(s)
	next.o3Code = -10003
	b.startConnectionLoop()
	_ = old.Close()
	awaitRecovery(t, func() bool { return b.ReconnectStatus().RequiresLogin })
	if attempts.Load() != 1 || s.acknowledged.Load() {
		t.Fatal("retried or acknowledged refused O3")
	}
	if !next.Closed() {
		t.Fatal("failed candidate left open")
	}
}
