package console

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"openopq/internal/api"
	"openopq/internal/device"
)

func hubCall(h *Hub, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://127.0.0.1:8090"+path, strings.NewReader(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func testHub(t *testing.T) *Hub {
	store, _ := device.New(t.TempDir())
	logs := NewLogBuffer(slog.LevelDebug)
	h := NewHub(Options{Store: store, Logs: logs, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), API: api.Options{OPQ: true, OneBot: true}, Factory: func(_ []byte, u uint32) (ManagedBot, error) { return &testBot{uin: u}, nil }})
	t.Cleanup(func() { _ = h.Close() })
	return h
}
func TestHubSimultaneousAccountsKeepIndependentConnections(t *testing.T) {
	h := testHub(t)
	a, err := h.Add("123456")
	if err != nil {
		t.Fatal(err)
	}
	b, err := h.Add("234567")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for _, v := range []struct{ id, account string }{{a, "123456"}, {b, "234567"}} {
		wg.Add(1)
		go func(id, account string) {
			defer wg.Done()
			w := hubCall(h, "POST", "/console/api/login?session="+id, `{"account":"`+account+`"}`)
			if w.Code != 202 {
				t.Errorf("login status %d: %s", w.Code, w.Body.String())
			}
		}(v.id, v.account)
	}
	wg.Wait()
	first, second := h.find(a).server, h.find(b).server
	waitPhase(t, first, "online")
	waitPhase(t, second, "online")
	if first.Snapshot().GUID == second.Snapshot().GUID {
		t.Fatal("accounts share identity")
	}
	for _, v := range []struct {
		id  string
		uin uint32
	}{{a, 123456}, {b, 234567}} {
		w := hubCall(h, "POST", "/accounts/"+v.id+"/onebot/v11/get_login_info", "{}")
		var r struct {
			Data struct {
				User uint32 `json:"user_id"`
			}
		}
		json.Unmarshal(w.Body.Bytes(), &r)
		if r.Data.User != v.uin {
			t.Fatalf("protocol routed to wrong account: %s", w.Body.String())
		}
	}
	first.opt.Logger.Info("first-only")
	second.opt.Logger.Info("second-only")
	if strings.Contains(hubCall(h, "GET", "/console/api/logs?session="+a, "").Body.String(), "second-only") {
		t.Fatal("logs mixed")
	}
	w := hubCall(h, "POST", "/console/api/disconnect?session="+a, "{}")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if !second.Snapshot().Online {
		t.Fatal("disconnect closed the other account")
	}
	if h.find(b).server.active.(*testBot).closed {
		t.Fatal("second connection was closed")
	}
	same, _ := h.Add("234567")
	if same != b {
		t.Fatal("saved account creates duplicate slot")
	}
	w = hubCall(h, "POST", "/console/api/login?session="+a, `{"account":"234567"}`)
	if w.Code != 409 {
		t.Fatal("duplicate account allowed")
	}
}
func TestHubRejectsCrossOriginAndUnknownSession(t *testing.T) {
	h := testHub(t)
	r := httptest.NewRequest("POST", "http://127.0.0.1:8090/console/api/sessions", strings.NewReader(`{}`))
	r.Header.Set("Origin", "http://example.com")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("cross origin accepted")
	}
	if hubCall(h, "POST", "/console/api/login?session=missing", `{}`).Code != 404 {
		t.Fatal("unknown session falls back to another account")
	}
	if hubCall(h, "GET", "/accounts/missing/onebot/v11/get_status", "").Code != 404 {
		t.Fatal("unknown protocol session accepted")
	}
}
func TestHubAttachPreservesExistingProcessAndUsesNewAssets(t *testing.T) {
	old := testServer(t, false, false)
	if err := old.Begin("123456"); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, old, "online")
	server := httptest.NewServer(old)
	defer server.Close()
	h := testHub(t)
	if err := h.Attach(server.URL); err != nil {
		t.Fatal(err)
	}
	w := hubCall(h, "POST", "/accounts/attached/onebot/v11/get_status", "{}")
	if !strings.Contains(w.Body.String(), `"online":true`) {
		t.Fatal(w.Body.String())
	}
	page := hubCall(h, "GET", "/console/?session=attached", "")
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), `id="session-select"`) {
		t.Fatal("old assets served")
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	if !old.Snapshot().Online {
		t.Fatal("closing hub disconnected attached process")
	}
}

func TestHubAttachesSpecificSessionsWithoutMixingOrClosingThem(t *testing.T) {
	makeSource := func(account string) (*Hub, *httptest.Server, string) {
		t.Helper()
		source := testHub(t)
		id, err := source.Add(account)
		if err != nil {
			t.Fatal(err)
		}
		if err := source.find(id).server.Begin(account); err != nil {
			t.Fatal(err)
		}
		waitPhase(t, source.find(id).server, "online")
		return source, httptest.NewServer(source), id
	}

	firstHub, firstServer, firstID := makeSource("123456")
	defer firstServer.Close()
	secondHub, secondServer, secondID := makeSource("234567")
	defer secondServer.Close()

	h := testHub(t)
	firstURL := firstServer.URL + "/accounts/" + firstID
	secondURL := secondServer.URL + "/accounts/" + secondID
	if err := h.Attach(firstURL); err != nil {
		t.Fatal(err)
	}
	if err := h.Attach(secondURL); err != nil {
		t.Fatal(err)
	}
	if err := h.Attach(firstURL); err != nil {
		t.Fatalf("attaching the same endpoint should be idempotent: %v", err)
	}
	if err := h.Attach(secondServer.URL + "/accounts/" + firstID); err == nil {
		t.Fatal("same account attached from a different endpoint")
	}
	if err := h.Attach(firstServer.URL + "/accounts/%2f" + firstID); err == nil {
		t.Fatal("encoded session path accepted")
	}

	var states []SessionState
	if err := json.Unmarshal(hubCall(h, http.MethodGet, "/console/api/sessions", "").Body.Bytes(), &states); err != nil {
		t.Fatal(err)
	}
	if len(states) != 2 || states[0].ID != "attached" || states[1].ID != "attached-1" {
		t.Fatalf("unexpected attached slots: %+v", states)
	}
	for _, tc := range []struct{ id string }{{"attached"}, {"attached-1"}} {
		w := hubCall(h, http.MethodPost, "/accounts/"+tc.id+"/onebot/v11/get_login_info", "{}")
		var response struct {
			Data struct {
				User uint32 `json:"user_id"`
			} `json:"data"`
		}
		want := uint32(123456)
		if tc.id == "attached-1" {
			want = 234567
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Data.User != want {
			t.Fatalf("slot %s routed to wrong account: %s (%v)", tc.id, w.Body.String(), err)
		}
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	if !firstHub.find(firstID).server.Snapshot().Online || !secondHub.find(secondID).server.Snapshot().Online {
		t.Fatal("closing attached management console disconnected a source session")
	}
}

func TestHubAccountClaimChecksBeforeSecondLogin(t *testing.T) {
	h := testHub(t)
	a, _ := h.Add("")
	b, _ := h.Add("")
	if err := h.claim(a, "123456"); err != nil {
		t.Fatal(err)
	}
	if err := h.claim(b, "123456"); err == nil {
		t.Fatal("same account accepted by two slots")
	}
	h.release(a)
	if err := h.claim(b, "123456"); err != nil {
		t.Fatal(err)
	}
}

func TestHubBlankLoginRetainsConfiguredAccountAndDevice(t *testing.T) {
	h := testHub(t)
	id, err := h.Add("123456")
	if err != nil {
		t.Fatal(err)
	}
	s := h.find(id).server
	guid := s.Snapshot().GUID
	for attempt := 0; attempt < 2; attempt++ {
		w := hubCall(h, "POST", "/console/api/login?session="+id, `{}`)
		if w.Code != http.StatusAccepted {
			t.Fatalf("login: %d %s", w.Code, w.Body.String())
		}
		waitPhase(t, s, "online")
		if got := s.Snapshot(); got.Account != "123456" || got.GUID != guid {
			t.Fatalf("blank input replaced configured identity: %+v", got)
		}
		w = hubCall(h, "POST", "/console/api/disconnect?session="+id, `{}`)
		if w.Code != http.StatusOK {
			t.Fatal(w.Body.String())
		}
	}
}
