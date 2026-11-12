package console

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"openopq/internal/api"
	"openopq/internal/bot"
	"openopq/internal/device"
)

type testBot struct {
	mu      sync.Mutex
	uin     uint32
	online  bool
	hold    bool
	closed  bool
	qrError error
}

type reconnectTestBot struct {
	*testBot
	recovery  bot.ReconnectStatus
	err       error
	heartbeat bot.HeartbeatStatus
}

func (b *reconnectTestBot) HeartbeatStatus() bot.HeartbeatStatus { return b.heartbeat }

func (b *reconnectTestBot) ReconnectStatus() bot.ReconnectStatus { return b.recovery }
func (b *reconnectTestBot) AuthenticationError() error           { return b.err }

func TestSnapshotReportsReconnectAndRestoredState(t *testing.T) {
	s := testServer(t, false, false)
	b := &reconnectTestBot{testBot: &testBot{uin: 123456}, recovery: bot.ReconnectStatus{Reconnecting: true, Attempt: 3}}
	s.mu.Lock()
	s.active = b
	s.state.Phase = "online"
	s.mu.Unlock()
	v := s.Snapshot()
	if v.Phase != "reconnecting" || v.Online || v.Reconnect.Attempt != 3 || !strings.Contains(v.Notice, "无需重新扫码") {
		t.Fatalf("wrong recovery state: %+v", v)
	}
	b.recovery = bot.ReconnectStatus{}
	b.online = true
	v = s.Snapshot()
	if v.Phase != "online" || !v.Online {
		t.Fatalf("not restored: %+v", v)
	}
	b.online = false
	b.recovery = bot.ReconnectStatus{RequiresLogin: true}
	b.err = errors.New("authentication refused")
	v = s.Snapshot()
	if v.Phase != "offline" || !v.Reconnect.RequiresLogin || v.Error == "" {
		t.Fatalf("lost terminal reason: %+v", v)
	}
}

func TestSnapshotExposesHeartbeatFailureAndRecovery(t *testing.T) {
	s := testServer(t, false, false)
	b := &reconnectTestBot{testBot: &testBot{uin: 123456, online: true}, heartbeat: bot.HeartbeatStatus{Stage: "failed", ConsecutiveFailures: 1}}
	s.mu.Lock()
	s.active = b
	s.state.Phase = "online"
	s.mu.Unlock()
	v := s.Snapshot()
	if !v.Online || v.Heartbeat.ConsecutiveFailures != 1 || !strings.Contains(v.Notice, "心跳") {
		t.Fatalf("missing transient warning: %+v", v)
	}
	b.online = false
	b.heartbeat.Stale = true
	b.recovery = bot.ReconnectStatus{Reconnecting: true, Attempt: 1}
	v = s.Snapshot()
	if v.Online || v.Phase != "reconnecting" || !v.Heartbeat.Stale {
		t.Fatalf("missing watchdog recovery: %+v", v)
	}
}

func (b *testBot) QRCode(context.Context) ([]byte, error) { return []byte("png"), b.qrError }
func (b *testBot) LoadSession(ctx context.Context) error {
	if b.hold {
		<-ctx.Done()
		return ctx.Err()
	}
	b.mu.Lock()
	b.online = true
	b.mu.Unlock()
	return nil
}
func (b *testBot) Self(context.Context) (bot.Self, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bot.Self{Uin: b.uin, Online: b.online}, nil
}
func (b *testBot) Groups(context.Context) ([]bot.Group, error)                { return nil, nil }
func (b *testBot) Call(context.Context, string, json.RawMessage) (any, error) { return nil, nil }
func (b *testBot) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.online = false
	b.closed = true
	return nil
}
func testServer(t *testing.T, hold, headless bool) *Server {
	t.Helper()
	store, err := device.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	logs := NewLogBuffer(slog.LevelDebug)
	logger := slog.New(MultiHandler(logs, slog.NewTextHandler(io.Discard, nil)))
	s := New(Options{Store: store, Logger: logger, Logs: logs, Headless: headless, API: api.Options{OPQ: true, OneBot: true}, Factory: func(_ []byte, uin uint32) (ManagedBot, error) { return &testBot{uin: uin, hold: hold}, nil }})
	t.Cleanup(func() { _ = s.Close() })
	return s
}
func call(s *Server, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://127.0.0.1:8087"+path, strings.NewReader(body))
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func waitPhase(t *testing.T, s *Server, phase string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s.Snapshot().Phase == phase {
			if phase != "restoring" && phase != "waiting" && phase != "authenticating" && phase != "securing" && phase != "registering" {
				if !s.op.TryLock() {
					time.Sleep(time.Millisecond)
					continue
				}
				s.op.Unlock()
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("wanted %s, got %+v", phase, s.Snapshot())
}
func TestLoginSwitchAndRepeatPreservesAccountDevices(t *testing.T) {
	s := testServer(t, false, false)
	if err := s.Begin("123456"); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, s, "online")
	first := s.Snapshot().GUID
	if err := s.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if err := s.Begin("234567"); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, s, "online")
	second := s.Snapshot().GUID
	if first == second {
		t.Fatal("different accounts share GUID")
	}
	if err := s.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if err := s.Begin("123456"); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, s, "online")
	if s.Snapshot().GUID != first {
		t.Fatal("repeat login changed device")
	}
	response := call(s, http.MethodPost, "/onebot/v11/get_status", "{}")
	if !strings.Contains(response.Body.String(), `"online":true`) {
		t.Fatal(response.Body.String())
	}
	if response := call(s, http.MethodGet, "/v1/clusterinfo", ""); !strings.Contains(response.Body.String(), `"Uin":123456`) {
		t.Fatal(response.Body.String())
	}
}
func TestCancelAndConcurrentLogin(t *testing.T) {
	s := testServer(t, true, false)
	if err := s.Begin("123456"); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, s, "waiting")
	if err := s.Begin("234567"); err == nil {
		t.Fatal("concurrent login accepted")
	}
	s.Cancel()
	waitPhase(t, s, "cancelled")
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if err := s.Select("234567"); err == nil {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("cancellation did not release login")
}
func TestLegacyLoginAndHeadlessControl(t *testing.T) {
	s := testServer(t, false, true)
	if w := call(s, "GET", "/v1/login/getqrcode?qq=123456", ""); !strings.Contains(w.Body.String(), "Qrcode") {
		t.Fatal(w.Body.String())
	}
	if w := call(s, "POST", "/v1/login/loadsession", "{}"); !strings.Contains(w.Body.String(), `"LoadSession":true`) {
		t.Fatal(w.Body.String())
	}
	if w := call(s, "GET", "/console/api/status", ""); !strings.Contains(w.Body.String(), `"online":true`) {
		t.Fatal(w.Body.String())
	}
	if w := call(s, "GET", "/console/", ""); strings.Contains(w.Header().Get("Content-Type"), "text/html") {
		t.Fatal("headless served webpage")
	}
}
func TestConsoleRejectsCrossOriginAndDNSRebinding(t *testing.T) {
	s := testServer(t, false, false)
	for _, test := range []struct{ host, origin string }{{"127.0.0.1:8087", "https://evil.example"}, {"evil.example:8087", ""}} {
		r := httptest.NewRequest("POST", "http://"+test.host+"/console/api/login", strings.NewReader(`{"account":"123456"}`))
		r.Header.Set("Origin", test.origin)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("accepted %+v", test)
		}
	}
	if w := call(s, "GET", "/console/", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "openopq") {
		t.Fatalf("console unavailable: %d", w.Code)
	}
}
func TestLogsBoundedAndCursorDoesNotRepeatEntries(t *testing.T) {
	b := NewLogBuffer(slog.LevelInfo)
	logger := slog.New(b).With("component", "test")
	for i := 0; i < 1100; i++ {
		logger.Info("event", "index", i)
	}
	entries := b.Since(0)
	if len(entries) != 1000 || entries[0].ID != 101 {
		t.Fatalf("unbounded buffer: %d", len(entries))
	}
	if len(b.Since(entries[len(entries)-1].ID)) != 0 {
		t.Fatal("cursor duplicated log entries")
	}
	if entries[0].Fields["component"] != "test" {
		t.Fatal("logger attributes lost")
	}
}

func TestAnonymousLoginBindsDeviceAfterScan(t *testing.T) {
	s := testServer(t, false, false)
	var requested uint32
	s.opt.Factory = func(guid []byte, uin uint32) (ManagedBot, error) {
		requested = uin
		if len(guid) != 16 {
			t.Errorf("anonymous GUID length: %d", len(guid))
		}
		return &testBot{uin: 123456}, nil
	}
	if err := s.Begin(""); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, s, "online")
	state := s.Snapshot()
	if requested != 0 || state.Account != "123456" || len(state.GUID) != 32 {
		t.Fatalf("anonymous identity: request=%d state=%+v", requested, state)
	}
	profiles, err := s.opt.Store.List()
	if err != nil || len(profiles) != 1 || profiles[0].GUID != state.GUID || profiles[0].LastLogin == nil || profiles[0].ComputerName == "" || profiles[0].ComputerName != state.ComputerName {
		t.Fatalf("bound profile: %+v, state=%+v, %v", profiles, state, err)
	}
	first := state.GUID
	if err := s.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if err := s.Begin(""); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, s, "online")
	if s.Snapshot().GUID == first {
		t.Fatal("new anonymous attempt reused an already bound device")
	}
}

func TestAnonymousLoginRetainsExistingAccountDevice(t *testing.T) {
	s := testServer(t, false, false)
	old, _, err := s.opt.Store.GetOrCreate("123456")
	if err != nil {
		t.Fatal(err)
	}
	s.opt.Factory = func(_ []byte, _ uint32) (ManagedBot, error) {
		return &testBot{uin: 123456}, nil
	}
	if err := s.Begin(""); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, s, "online")
	state := s.Snapshot()
	profiles, err := s.opt.Store.List()
	if err != nil || len(profiles) != 1 || profiles[0].GUID != old.GUID || profiles[0].LastLogin != nil {
		t.Fatalf("existing profile replaced or incorrectly marked used: %+v %v", profiles, err)
	}
	if state.GUID == old.GUID || state.Notice == "" || state.Account != old.Account {
		t.Fatalf("new device login not explained: %+v", state)
	}
	if err := s.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if err := s.Begin("123456"); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, s, "online")
	if s.Snapshot().GUID != old.GUID || s.Snapshot().Notice != "" {
		t.Fatalf("explicit selection did not restore saved identity: %+v", s.Snapshot())
	}
}

func TestAnonymousCancelAndRetryRetainsUnboundDevice(t *testing.T) {
	s := testServer(t, true, false)
	if w := call(s, http.MethodPost, "/console/api/login", `{"account":""}`); w.Code != http.StatusAccepted {
		t.Fatal(w.Body.String())
	}
	waitPhase(t, s, "waiting")
	first := s.Snapshot().GUID
	if guid, err := hex.DecodeString(first); err != nil || len(guid) != 16 || s.Snapshot().Account != "" {
		t.Fatalf("invalid anonymous device: %+v", s.Snapshot())
	}
	profiles, err := s.opt.Store.List()
	if err != nil || len(profiles) != 0 {
		t.Fatalf("profile created before scan: %+v %v", profiles, err)
	}
	s.Cancel()
	waitPhase(t, s, "cancelled")
	if err := s.Begin(""); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, s, "waiting")
	if s.Snapshot().GUID != first {
		t.Fatal("retry changed the unbound device")
	}
	s.Cancel()
	waitPhase(t, s, "cancelled")
}

func TestSelectedAccountMismatchRetainsSavedIdentity(t *testing.T) {
	s := testServer(t, false, false)
	s.opt.Factory = func(_ []byte, _ uint32) (ManagedBot, error) {
		return &testBot{uin: 234567}, nil
	}
	if err := s.Begin("123456"); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, s, "error")
	state := s.Snapshot()
	if state.Account != "123456" || !strings.Contains(state.Error, "does not match") {
		t.Fatalf("account mismatch accepted: %+v", state)
	}
	profiles, err := s.opt.Store.List()
	if err != nil || len(profiles) != 1 || profiles[0].Account != "123456" || profiles[0].GUID != state.GUID || profiles[0].LastLogin != nil {
		t.Fatalf("mismatch changed saved identity: %+v %v", profiles, err)
	}
}

func TestAnonymousFailedQRRetryRetainsDevice(t *testing.T) {
	s := testServer(t, true, false)
	firstAttempt := true
	s.opt.Factory = func(_ []byte, _ uint32) (ManagedBot, error) {
		b := &testBot{hold: true}
		if firstAttempt {
			b.qrError = errors.New("temporary QR transport failure")
			firstAttempt = false
		}
		return b, nil
	}
	if err := s.Begin(""); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, s, "error")
	first := s.Snapshot().GUID
	if err := s.Begin(""); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, s, "waiting")
	if first == "" || s.Snapshot().GUID != first {
		t.Fatal("QR failure rotated anonymous device")
	}
	s.Cancel()
	waitPhase(t, s, "cancelled")
}

type progressBot struct {
	*testBot
	progress func(string)
	arrived  chan string
	advance  chan struct{}
}

func (b *progressBot) SetLoginProgress(callback func(string)) {
	b.mu.Lock()
	b.progress = callback
	b.mu.Unlock()
}

func (b *progressBot) LoadSession(ctx context.Context) error {
	for _, phase := range []string{"authenticating", "securing", "registering"} {
		b.mu.Lock()
		callback := b.progress
		b.mu.Unlock()
		if callback != nil {
			callback(phase)
		}
		select {
		case b.arrived <- phase:
		case <-ctx.Done():
			return ctx.Err()
		}
		select {
		case <-b.advance:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return b.testBot.LoadSession(ctx)
}

func TestLoginProgressClearsQRAndWaitsForOnlineRegistration(t *testing.T) {
	s := testServer(t, false, false)
	b := &progressBot{testBot: &testBot{uin: 123456}, arrived: make(chan string, 1), advance: make(chan struct{})}
	s.opt.Factory = func(_ []byte, _ uint32) (ManagedBot, error) { return b, nil }
	if err := s.Begin(""); err != nil {
		t.Fatal(err)
	}
	var stale func(string)
	for _, phase := range []string{"authenticating", "securing", "registering"} {
		select {
		case actual := <-b.arrived:
			if actual != phase {
				t.Fatalf("progress order: %s, wanted %s", actual, phase)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("missing progress %s", phase)
		}
		state := s.Snapshot()
		if state.Phase != phase || state.Online || state.QR != "" || state.QRExpires != nil {
			t.Fatalf("progress prematurely online or still has QR: %+v", state)
		}
		b.mu.Lock()
		stale = b.progress
		b.mu.Unlock()
		b.advance <- struct{}{}
	}
	waitPhase(t, s, "online")
	b.mu.Lock()
	cleared := b.progress == nil
	b.mu.Unlock()
	if !cleared {
		t.Fatal("completed flow retained progress callback")
	}
	stale("securing")
	if s.Snapshot().Phase != "online" {
		t.Fatal("stale callback changed completed flow")
	}
}

func TestSelectExposesPersistedComputerName(t *testing.T) {
	s := testServer(t, false, false)
	if err := s.Select("123456"); err != nil {
		t.Fatal(err)
	}
	state := s.Snapshot()
	if state.ComputerName == "" || !strings.Contains(state.ComputerName, "'s ") {
		t.Fatalf("computer name missing: %+v", state)
	}
	profiles, err := s.opt.Store.List()
	if err != nil || len(profiles) != 1 || profiles[0].ComputerName != state.ComputerName {
		t.Fatalf("profile name: %+v %v", profiles, err)
	}
}

func TestCancelledLoginIgnoresStaleProgress(t *testing.T) {
	s := testServer(t, false, false)
	b := &progressBot{testBot: &testBot{uin: 123456}, arrived: make(chan string, 1), advance: make(chan struct{})}
	s.opt.Factory = func(_ []byte, _ uint32) (ManagedBot, error) { return b, nil }
	if err := s.Begin(""); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, s, "authenticating")
	b.mu.Lock()
	stale := b.progress
	b.mu.Unlock()
	s.Cancel()
	waitPhase(t, s, "cancelled")
	stale("registering")
	state := s.Snapshot()
	if state.Phase != "cancelled" || state.Online {
		t.Fatalf("cancelled flow accepted stale progress: %+v", state)
	}
}
