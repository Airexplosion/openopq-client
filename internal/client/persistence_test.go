package client

import (
	"bytes"
	"context"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"openopq/internal/loginstate"
	"openopq/internal/protocol"
)

func savedBot(t *testing.T) (*Bot, *loginstate.Store) {
	t.Helper()
	b, _, _ := recoveryBot(t, func(context.Context) (Connection, error) { return nil, io.EOF })
	store, err := loginstate.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b.SetLoginStore(store)
	b.credentials = protocol.Credentials{Token010A: b.account.Token, Token0143: b.account.D2, SessionKey: b.account.SessionKey, Uid: b.account.UID, NickName: "saved nickname"}
	if err := b.PersistLogin(); err != nil {
		t.Fatal(err)
	}
	return b, store
}

func freshBot(t *testing.T, original *Bot, store *loginstate.Store) (*Bot, *recoveryWire, *recoverySigner) {
	t.Helper()
	options := original.options
	options.Session = Session{AppID: original.account.AppID, MsfID: original.account.MsfID, NTSign: original.account.NTSign, Framing: original.account.Framing}
	options.ExpectedUin = original.account.Uin
	signer := &recoverySigner{}
	wire := newRecoveryWire(signer)
	options.Signer = signer
	options.LoginStore = store
	options.Dial = func(context.Context) (Connection, error) { return wire, nil }
	b := New(options)
	t.Cleanup(func() { _ = b.Close() })
	return b, wire, signer
}

func TestPersistedLoginRestoresFreshBotWithoutQRAndPreservesIdentity(t *testing.T) {
	old, store := savedBot(t)
	original := old.Session()
	if err := old.Suspend(); err != nil {
		t.Fatal(err)
	}
	if old.options.Signer.(*recoverySigner).released.Load() != 0 {
		t.Fatal("shutdown revoked signer resume lease")
	}
	fresh, wire, _ := freshBot(t, old, store)
	if len(fresh.Session().SessionKey) != 0 || fresh.Credentials().Complete() {
		t.Fatal("fresh bot inherited in-memory credentials")
	}
	if err := fresh.ResumeLogin(context.Background()); err != nil {
		t.Fatal(err)
	}
	self, err := fresh.Self(context.Background())
	if err != nil || !self.Online || self.Uin != original.Uin || self.Nickname != "saved nickname" {
		t.Fatalf("not restored: %+v, %v", self, err)
	}
	got := fresh.Session()
	if got.Device != original.Device || got.UID != original.UID || !bytes.Equal(got.D2, original.D2) || !bytes.Equal(got.SessionKey, original.SessionKey) {
		t.Fatal("restored different identity/tickets")
	}
	wire.mu.Lock()
	defer wire.mu.Unlock()
	if !restoreRegisteredWithoutQR(wire.commands) || !bytes.Equal(wire.key, original.SessionKey) {
		t.Fatalf("restore used login/QR or wrong key: %v", wire.commands)
	}
}

func restoreRegisteredWithoutQR(commands []string) bool {
	registered := false
	for _, cmd := range commands {
		if cmd == protocol.WTLoginCommand || cmd == protocol.TransEmpCommand {
			return false
		}
		if cmd == protocol.RegisterCommand {
			registered = true
		}
	}
	return registered
}

func TestPauseManualResumeForgetAndAuthenticationInvalidation(t *testing.T) {
	old, store := savedBot(t)
	if err := old.PauseLogin(); err != nil {
		t.Fatal(err)
	}
	if !old.SavedLoginStatus().Restorable || old.SavedLoginStatus().AutoResume {
		t.Fatal("pause did not retain manually restorable login")
	}
	_ = old.Close()
	fresh, wire, _ := freshBot(t, old, store)
	if err := fresh.ResumeLogin(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := fresh.PersistLogin(); err != nil {
		t.Fatal(err)
	}
	if !fresh.SavedLoginStatus().AutoResume {
		t.Fatal("accepted manual resume not re-enabled")
	}
	wire.push <- protocol.SSOResponse{Code: -10003, Command: "test"}
	awaitRecovery(t, func() bool { return fresh.ReconnectStatus().RequiresLogin })
	if fresh.SavedLoginStatus().Restorable {
		t.Fatal("rejected login remained restorable")
	}
	_ = fresh.Close()
	if err := fresh.ForgetLogin(); err != nil {
		t.Fatal(err)
	}
	if fresh.SavedLoginStatus().Saved || fresh.Credentials().Complete() {
		t.Fatal("forget retained login")
	}
}

func TestResumeRefusalDoesNotRetryButNetworkFailureKeepsCache(t *testing.T) {
	for _, refused := range []bool{true, false} {
		t.Run(map[bool]string{true: "auth", false: "network"}[refused], func(t *testing.T) {
			old, store := savedBot(t)
			_ = old.Suspend()
			fresh, wire, _ := freshBot(t, old, store)
			var calls atomic.Int32
			fresh.options.Dial = func(context.Context) (Connection, error) {
				calls.Add(1)
				if !refused {
					return nil, io.EOF
				}
				return wire, nil
			}
			wire.code = -10003
			if err := fresh.ResumeLogin(context.Background()); err == nil {
				t.Fatal("bad restore accepted")
			}
			want := int32(1)
			if !refused {
				want = 3
			}
			if calls.Load() != want {
				t.Fatalf("unexpected restore dials: %d", calls.Load())
			}
			if fresh.SavedLoginStatus().Restorable == refused {
				t.Fatal("cache invalidation classification incorrect")
			}
		})
	}
}

func TestResumeCannotCrossAccountDeviceOrVersionAndCanBeCancelled(t *testing.T) {
	for _, mode := range []string{"account", "guid", "version", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			old, store := savedBot(t)
			_ = old.Suspend()
			fresh, wire, _ := freshBot(t, old, store)
			var calls atomic.Int32
			fresh.options.Dial = func(context.Context) (Connection, error) { calls.Add(1); return wire, nil }
			switch mode {
			case "account":
				fresh.options.ExpectedUin++
			case "guid":
				fresh.options.Device.Guid = bytes.Repeat([]byte{9}, 16)
			case "version":
				fresh.options.LoginIdentity = "foreign"
			case "cancel":
				wire.blockRegister = true
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- fresh.ResumeLogin(ctx) }()
			if mode == "cancel" {
				select {
				case <-wire.entered:
					cancel()
				case <-time.After(time.Second):
					t.Fatal("restore did not reach Register")
				}
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("invalid/cancelled restore succeeded")
				}
			case <-time.After(time.Second):
				t.Fatal("restore not cancelled")
			}
			if mode != "cancel" && calls.Load() != 0 {
				t.Fatal("mismatched cache reached network")
			}
			if mode == "cancel" && (!wire.Closed() || fresh.ReconnectStatus().Reconnecting) {
				t.Fatal("cancel left candidate alive")
			}
		})
	}
}

func TestFreshSavedLoginCanCompleteSignerO3Recovery(t *testing.T) {
	old, store := savedBot(t)
	_ = old.Suspend()
	fresh, _, signer := freshBot(t, old, store)
	signer.secureReady = make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := fresh.ResumeLogin(ctx); err != nil {
		t.Fatal(err)
	}
	if !signer.acknowledged.Load() {
		t.Fatal("saved login skipped required O3 recovery")
	}
}

func TestCloseInterruptsSavedLoginRestore(t *testing.T) {
	old, store := savedBot(t)
	_ = old.Suspend()
	fresh, wire, _ := freshBot(t, old, store)
	wire.blockRegister = true
	finished := make(chan error, 1)
	go func() { finished <- fresh.ResumeLogin(context.Background()) }()
	<-wire.entered
	closed := make(chan struct{})
	go func() { _ = fresh.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close blocked on restore")
	}
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("cancelled restore returned success")
		}
	case <-time.After(time.Second):
		t.Fatal("restore worker leaked")
	}
	if !wire.Closed() || fresh.ReconnectStatus().Reconnecting {
		t.Fatal("closed account resurrected")
	}
}
