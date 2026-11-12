package console

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"openopq/internal/api"
	"openopq/internal/bot"
	"openopq/internal/device"
	"openopq/internal/loginstate"
	"openopq/internal/protocol"
)

func fmtAccount(uin uint32) string { return strconv.FormatUint(uint64(uin), 10) }

type persistentTestBot struct {
	*testBot
	store                  *loginstate.Store
	guid                   string
	qr, restores, suspends *atomic.Int32
	block                  bool
}

func (b *persistentTestBot) QRCode(ctx context.Context) ([]byte, error) {
	b.qr.Add(1)
	return b.testBot.QRCode(ctx)
}
func (b *persistentTestBot) PersistLogin() error {
	return b.store.Save(loginstate.Record{Metadata: loginstate.Metadata{Account: fmtAccount(b.uin), GUID: b.guid, Identity: strings.Repeat("a", 64)}, Credentials: protocol.Credentials{Token010A: []byte("ticket"), Token0143: []byte("d2"), SessionKey: bytes.Repeat([]byte{2}, 16)}})
}
func (b *persistentTestBot) SavedLoginStatus() bot.SavedLoginStatus {
	r, err := b.store.Load(fmtAccount(b.uin), b.guid, strings.Repeat("a", 64))
	return bot.SavedLoginStatus{Saved: !errors.Is(err, loginstate.ErrMissing), Restorable: err == nil, AutoResume: r.AutoResume}
}
func (b *persistentTestBot) ResumeLogin(ctx context.Context) error {
	if _, err := b.store.Load(fmtAccount(b.uin), b.guid, strings.Repeat("a", 64)); err != nil {
		return err
	}
	b.restores.Add(1)
	if b.block {
		<-ctx.Done()
		return ctx.Err()
	}
	b.mu.Lock()
	b.online = true
	b.mu.Unlock()
	return nil
}
func (b *persistentTestBot) PauseLogin() error  { return b.store.Pause(fmtAccount(b.uin), b.guid) }
func (b *persistentTestBot) ForgetLogin() error { return b.store.Delete(fmtAccount(b.uin), b.guid) }
func (b *persistentTestBot) Suspend() error     { b.suspends.Add(1); return b.testBot.Close() }

type persistenceCounts struct{ qr, restores, suspends atomic.Int32 }

func persistentHub(t *testing.T, dir string, counts *persistenceCounts, block bool) *Hub {
	t.Helper()
	profiles, err := device.New(filepath.Join(dir, "devices"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := loginstate.New(filepath.Join(dir, "qq-logins"))
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := NewHub(Options{Store: profiles, LoginStore: store, Logger: logger, Logs: NewLogBuffer(slog.LevelDebug), API: api.Options{OPQ: true, OneBot: true, OneBot12: true}, Factory: func(guid []byte, uin uint32) (ManagedBot, error) {
		return &persistentTestBot{testBot: &testBot{uin: uin}, store: store, guid: hex.EncodeToString(guid), qr: &counts.qr, restores: &counts.restores, suspends: &counts.suspends, block: block}, nil
	}})
	t.Cleanup(func() { _ = h.Close() })
	return h
}

func TestHubRestartRestoresMultipleAccountsWithoutQRAndDisconnectPauses(t *testing.T) {
	dir := t.TempDir()
	first := persistentHub(t, dir, &persistenceCounts{}, false)
	for _, account := range []string{"123456", "234567"} {
		if err := first.BeginAccount(account); err != nil {
			t.Fatal(err)
		}
	}
	for _, s := range first.slots {
		if s.account != "" {
			waitPhase(t, s.server, "online")
		}
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	counts := &persistenceCounts{}
	second := persistentHub(t, dir, counts, false)
	if err := second.ResumeSavedLogins(""); err != nil {
		t.Fatal(err)
	}
	for _, s := range second.slots {
		waitPhase(t, s.server, "online")
	}
	if counts.restores.Load() != 2 || counts.qr.Load() != 0 {
		t.Fatal("restart used QR or dropped an account")
	}
	paused := second.defaultSlot()
	if err := paused.server.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if paused.server.Snapshot().SavedLogin.AutoResume {
		t.Fatal("disconnect did not pause startup restore")
	}
	_ = second.Close()
	thirdCounts := &persistenceCounts{}
	third := persistentHub(t, dir, thirdCounts, false)
	if err := third.ResumeSavedLogins(""); err != nil {
		t.Fatal(err)
	}
	for _, s := range third.slots {
		if s.account != "" {
			waitPhase(t, s.server, "online")
		}
	}
	if thirdCounts.restores.Load() != 1 {
		t.Fatal("explicitly disconnected account restored itself")
	}
	if err := third.defaultSlot().server.ForgetLogin(); err != nil {
		t.Fatal(err)
	}
	if third.defaultSlot().server.Snapshot().SavedLogin.Saved {
		t.Fatal("forget endpoint retained tickets")
	}
}

func TestCancelSavedRestoreKeepsDeviceButPausesAutomaticLogin(t *testing.T) {
	dir := t.TempDir()
	first := persistentHub(t, dir, &persistenceCounts{}, false)
	if err := first.BeginAccount("123456"); err != nil {
		t.Fatal(err)
	}
	for _, s := range first.slots {
		if s.account == "123456" {
			waitPhase(t, s.server, "online")
		}
	}
	_ = first.Close()
	h := persistentHub(t, dir, &persistenceCounts{}, true)
	if err := h.ResumeSavedLogins(""); err != nil {
		t.Fatal(err)
	}
	s := h.defaultSlot().server
	waitPhase(t, s, "restoring")
	s.Cancel()
	waitPhase(t, s, "cancelled")
	if status := s.Snapshot(); status.SavedLogin.AutoResume || !status.SavedLogin.Restorable || status.GUID == "" {
		t.Fatalf("cancel lost saved identity: %+v", status)
	}
}

func TestShutdownDuringSavedRestoreKeepsAutoResumeEnabled(t *testing.T) {
	dir := t.TempDir()
	first := persistentHub(t, dir, &persistenceCounts{}, false)
	if err := first.BeginAccount("123456"); err != nil {
		t.Fatal(err)
	}
	for _, s := range first.slots {
		if s.account == "123456" {
			waitPhase(t, s.server, "online")
		}
	}
	_ = first.Close()
	second := persistentHub(t, dir, &persistenceCounts{}, true)
	if err := second.ResumeSavedLogins(""); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, second.defaultSlot().server, "restoring")
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if !second.defaultSlot().server.Snapshot().SavedLogin.AutoResume {
		t.Fatal("shutdown treated as user cancellation")
	}
}

func TestResumeAndForgetControlEndpoints(t *testing.T) {
	h := persistentHub(t, t.TempDir(), &persistenceCounts{}, false)
	if err := h.BeginAccount("123456"); err != nil {
		t.Fatal(err)
	}
	var target *slot
	for _, s := range h.slots {
		if s.account == "123456" {
			target = s
		}
	}
	waitPhase(t, target.server, "online")
	if w := hubCall(h, "POST", "/console/api/disconnect?session="+target.id, "{}"); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := hubCall(h, "POST", "/console/api/resume?session="+target.id, "{}"); w.Code != 202 {
		t.Fatal(w.Body.String())
	}
	waitPhase(t, target.server, "online")
	if w := hubCall(h, "POST", "/console/api/forget-login?session="+target.id, "{}"); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if target.server.Snapshot().SavedLogin.Saved {
		t.Fatal("HTTP forget retained cache")
	}
}

func TestHubRestoresSavedDeviceWhenProfileGUIDDiffers(t *testing.T) {
	dir := t.TempDir()
	first := persistentHub(t, dir, &persistenceCounts{}, false)
	if err := first.BeginAccount("123456"); err != nil {
		t.Fatal(err)
	}
	for _, s := range first.slots {
		if s.account == "123456" {
			waitPhase(t, s.server, "online")
		}
	}
	savedGUID := ""
	for _, s := range first.slots {
		if s.account == "123456" {
			savedGUID = s.server.Snapshot().GUID
		}
	}
	if savedGUID == "" {
		t.Fatal("missing saved device")
	}
	_ = first.Close()
	file := filepath.Join(dir, "devices", "123456.json")
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var p device.Profile
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	p.GUID = strings.Repeat("ab", 16)
	rewritten, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, rewritten, 0600); err != nil {
		t.Fatal(err)
	}
	counts := &persistenceCounts{}
	second := persistentHub(t, dir, counts, false)
	if err := second.ResumeSavedLogins(""); err != nil {
		t.Fatal(err)
	}
	var restored *Server
	for _, s := range second.slots {
		if s.account == "123456" {
			restored = s.server
		}
	}
	if restored == nil {
		t.Fatal("saved account was not reopened")
	}
	waitPhase(t, restored, "online")
	if counts.restores.Load() != 1 || counts.qr.Load() != 0 {
		t.Fatal("restart used QR or skipped the saved device")
	}
	if restored.Snapshot().GUID != savedGUID {
		t.Fatalf("restored a different device: %s", restored.Snapshot().GUID)
	}
}
