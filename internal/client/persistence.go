package client

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strconv"

	"openopq/internal/bot"
	"openopq/internal/loginstate"
	"openopq/internal/protocol"
)

func (b *Bot) savedIdentity() (string, string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	uin := b.account.Uin
	if uin == 0 {
		uin = b.options.ExpectedUin
	}
	return strconv.FormatUint(uint64(uin), 10), hex.EncodeToString(b.options.Device.Guid)
}

// PersistLogin is called after the console has verified/bound the device profile.
func (b *Bot) PersistLogin() error {
	if b.options.LoginStore == nil {
		return nil
	}
	b.mu.Lock()
	if !b.online || !b.credentials.Complete() {
		b.mu.Unlock()
		return errors.New("cannot save an unregistered QQ login")
	}
	r := loginstate.Record{Metadata: loginstate.Metadata{Account: strconv.FormatUint(uint64(b.account.Uin), 10), GUID: hex.EncodeToString(b.options.Device.Guid), Identity: b.options.LoginIdentity}, Credentials: b.credentials}
	b.mu.Unlock()
	if err := b.options.LoginStore.Save(r); err != nil {
		return err
	}
	if logger := b.options.Logger; logger != nil {
		logger.Info("QQ login tickets saved for the next start", "account", r.Account)
	}
	return nil
}

func (b *Bot) SavedLoginStatus() bot.SavedLoginStatus {
	if b.options.LoginStore == nil {
		return bot.SavedLoginStatus{Reason: "disabled"}
	}
	a, g := b.savedIdentity()
	if a == "0" {
		return bot.SavedLoginStatus{}
	}
	r, err := b.options.LoginStore.Load(a, g, b.options.LoginIdentity)
	if errors.Is(err, loginstate.ErrMissing) {
		return bot.SavedLoginStatus{}
	}
	if err != nil {
		return bot.SavedLoginStatus{Saved: true, Reason: err.Error()}
	}
	return bot.SavedLoginStatus{Saved: true, Restorable: true, AutoResume: r.AutoResume}
}

func (b *Bot) PauseLogin() error {
	if b.options.LoginStore == nil {
		return nil
	}
	a, g := b.savedIdentity()
	if a == "0" {
		return nil
	}
	return b.options.LoginStore.Pause(a, g)
}

func (b *Bot) ForgetLogin() error {
	if b.options.LoginStore == nil {
		return nil
	}
	a, g := b.savedIdentity()
	if a == "0" {
		return nil
	}
	b.mu.Lock()
	active := b.connection != nil || b.recoveryDone != nil
	b.mu.Unlock()
	if active {
		return errors.New("disconnect before forgetting QQ login")
	}
	if err := b.options.LoginStore.Delete(a, g); err != nil {
		return err
	}
	b.mu.Lock()
	b.credentials = protocol.Credentials{}
	b.account.Token = nil
	b.account.D2 = nil
	b.account.SessionKey = nil
	b.mu.Unlock()
	return nil
}

func (b *Bot) invalidateSavedLogin() {
	if b.options.LoginStore == nil {
		return
	}
	a, g := b.savedIdentity()
	if a == "0" {
		return
	}
	if err := b.options.LoginStore.Invalidate(a, g, "QQ registration or authentication refused; scan again"); err != nil {
		b.reconnectLog(slog.LevelError, "could not invalidate saved QQ login", "account", a, "error", err)
	}
}

// ResumeLogin skips QR and wtlogin. A valid saved identity is restored on a
// fresh connection: the login ECDH greeting, then Register/O3 with stored tickets.
func (b *Bot) ResumeLogin(ctx context.Context) error {
	if b.options.LoginStore == nil {
		return loginstate.ErrMissing
	}
	a, g := b.savedIdentity()
	r, err := b.options.LoginStore.Load(a, g, b.options.LoginIdentity)
	if err != nil {
		return err
	}
	uin, _ := strconv.ParseUint(r.Account, 10, 32)
	b.mu.Lock()
	if b.connection != nil || b.recoveryDone != nil || b.login != nil {
		b.mu.Unlock()
		return errors.New("disconnect the current session before restoring login")
	}
	guard := b.accountGuard
	b.mu.Unlock()
	if guard != nil {
		if err = guard(uint32(uin)); err != nil {
			return err
		}
	}
	options := b.options.Reconnect.defaults()
	done := make(chan struct{})
	restoreCtx, cancel := context.WithCancel(ctx)
	b.mu.Lock()
	if b.connection != nil || b.recoveryDone != nil || restoreCtx.Err() != nil {
		b.mu.Unlock()
		cancel()
		return errors.New("saved login restore cancelled or already running")
	}
	b.credentials = r.Credentials
	b.account.Uin = uint32(uin)
	b.account.UID = r.Credentials.Uid
	b.account.Token = r.Credentials.Token010A
	b.account.D2 = r.Credentials.Token0143
	b.account.SessionKey = r.Credentials.SessionKey
	b.nickname = r.Credentials.NickName
	b.recoveryDone, b.recoveryCancel = done, cancel
	b.recovery = bot.ReconnectStatus{Reconnecting: true, Attempt: 1}
	b.mu.Unlock()
	const maxAttempts = 3
	var fatal bool
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if restoreCtx.Err() != nil {
			err = restoreCtx.Err()
			break
		}
		b.mu.Lock()
		if b.recoveryDone == done {
			b.recovery = bot.ReconnectStatus{Reconnecting: true, Attempt: attempt}
		}
		b.mu.Unlock()
		attemptCtx, attemptCancel := context.WithTimeout(restoreCtx, options.AttemptTimeout)
		_, _, err, fatal = b.reconnectAttempt(attemptCtx, done, true)
		attemptCancel()
		if err == nil || fatal || restoreCtx.Err() != nil {
			break
		}
		b.reconnectLog(slog.LevelWarn, "saved QQ login restore attempt failed", "account", a, "attempt", attempt, "error", err)
		if attempt < maxAttempts && !waitRecovery(restoreCtx, options.MinDelay) {
			err = restoreCtx.Err()
			break
		}
	}
	if fatal {
		b.invalidateSavedLogin()
	}
	b.mu.Lock()
	if b.recoveryDone == done {
		b.recoveryDone = nil
		b.recoveryCancel = nil
		if err != nil {
			b.recovery = bot.ReconnectStatus{LastError: err.Error(), RequiresLogin: fatal}
			if fatal {
				b.terminalErr = err
			}
		}
	}
	b.mu.Unlock()
	close(done)
	cancel()
	if err != nil {
		return fmt.Errorf("restore saved QQ login: %w", err)
	}
	b.reconnectLog(slog.LevelInfo, "saved QQ login restored", "account", a)
	b.startConnectionLoop()
	return nil
}
