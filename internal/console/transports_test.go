package console

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"openopq/internal/api"
	"openopq/internal/protocol"
)

func freeProtocolPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return p
}

func readTransports(t *testing.T, h *Hub, id string) transportSnapshot {
	t.Helper()
	w := hubCall(h, "GET", "/console/api/transports?session="+id, "")
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var snapshot transportSnapshot
	if err := json.Unmarshal(w.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestTransportPortConflictRollsBackEntirePatch(t *testing.T) {
	h := testHub(t)
	id, _ := h.Add("123456")
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port
	body := fmt.Sprintf(`{"services":{"opq_http":{"enabled":false,"port":0},"onebot_http":{"enabled":true,"port":%d}}}`, port)
	w := hubCall(h, "POST", "/console/api/transports?session="+id, body)
	if w.Code != 409 {
		t.Fatalf("wanted conflict: %d %s", w.Code, w.Body.String())
	}
	s := readTransports(t, h, id)
	if !s.Services["opq_http"].Enabled || s.Services["onebot_http"].Port != 0 {
		t.Fatal("failed transaction changed services")
	}
	for _, invalid := range []string{
		`{"services":{"opq_http":{"enabled":false}}}`,
		`{"services":{"opq_http":{"enabled":false,"port":-1}}}`,
		`{"services":{"opq_http":{"enabled":false,"port":0,"typo":1}}}`,
		`{"services":{"wrong":{"enabled":false,"port":0}}}`,
		`{"services":{"opq_http":{"enabled":false,"port":0}}} {}`,
	} {
		if w := hubCall(h, "POST", "/console/api/transports?session="+id, invalid); w.Code < 400 {
			t.Fatalf("accepted invalid config %s", invalid)
		}
	}
	if !readTransports(t, h, id).Services["opq_http"].Enabled {
		t.Fatal("invalid request disabled service")
	}
}

func TestDedicatedPortSharesTransportsButNeverAccountsOrConsole(t *testing.T) {
	h := testHub(t)
	a, _ := h.Add("123456")
	b, _ := h.Add("234567")
	port := freeProtocolPort(t)
	w := hubCall(h, "POST", "/console/api/transports?session="+a, fmt.Sprintf(`{"services":{"onebot_http":{"enabled":true,"port":%d},"onebot_ws":{"enabled":true,"port":%d}}}`, port, port))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	client := &http.Client{Timeout: 2 * time.Second}
	url := fmt.Sprintf("http://127.0.0.1:%d", port)
	response, err := client.Post(url+"/onebot/v11/get_login_info", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if !strings.Contains(string(data), `"user_id":123456`) {
		t.Fatalf("wrong dedicated account: %s", data)
	}
	for _, tc := range []struct {
		path   string
		status int
	}{{"/onebot/v11/get_status?qq=234567", 400}, {"/onebot/v11/get_status?session=" + b, 400}, {"/console/api/status", 404}, {"/ws", 503}} {
		r, err := client.Get(url + tc.path)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if r.StatusCode != tc.status {
			t.Fatalf("%s got %d", tc.path, r.StatusCode)
		}
	}
	if w := hubCall(h, "POST", "/accounts/"+a+"/onebot/v11/get_status", `{}`); w.Code != 503 {
		t.Fatal("old shared endpoint still exposed")
	}
	if w := hubCall(h, "POST", "/accounts/"+b+"/onebot/v11/get_login_info", `{}`); !strings.Contains(w.Body.String(), `"user_id":234567`) {
		t.Fatal("other account affected")
	}
	snapshot := readTransports(t, h, a)
	if snapshot.Services["onebot_http"].TotalRequests != 1 || snapshot.Services["onebot_http"].ActiveRequests != 0 || snapshot.Services["onebot_http"].Connections != 0 {
		t.Fatal("HTTP requests reported as clients")
	}
	if !snapshot.Services["onebot_ws"].Listening {
		t.Fatal("could not share port within account")
	}
	if w := hubCall(h, "POST", "/console/api/transports?session="+b, fmt.Sprintf(`{"services":{"onebot_http":{"enabled":true,"port":%d}}}`, port)); w.Code != 409 {
		t.Fatal("another account took port")
	}
}

type transportEventBot struct{ *testBot }

func (b *transportEventBot) Subscribe() (<-chan protocol.MessageEvent, func()) {
	ch := make(chan protocol.MessageEvent)
	return ch, func() { close(ch) }
}

func TestDisablingAccountWebSocketClosesOnlyItsConnections(t *testing.T) {
	h := testHub(t)
	a, _ := h.Add("123456")
	b, _ := h.Add("234567")
	for _, id := range []string{a, b} {
		s := h.find(id).server
		s.api = api.NewServer(&transportEventBot{&testBot{}}, s.opt.Logger, api.Options{OPQ: true, OneBot: true})
	}
	server := httptest.NewServer(h)
	defer server.Close()
	open := func(id string) net.Conn {
		c, err := net.DialTimeout("tcp", strings.TrimPrefix(server.URL, "http://"), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(c, "GET /accounts/%s/ws HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n", id, strings.TrimPrefix(server.URL, "http://"))
		_ = c.SetReadDeadline(time.Now().Add(time.Second))
		r, err := http.ReadResponse(bufio.NewReader(c), nil)
		if err != nil || r.StatusCode != 101 {
			t.Fatalf("upgrade failed: %v", err)
		}
		_ = c.SetReadDeadline(time.Time{})
		return c
	}
	ca, cb := open(a), open(b)
	defer ca.Close()
	defer cb.Close()
	if readTransports(t, h, a).Services["opq_ws"].Connections != 1 || readTransports(t, h, b).Services["opq_ws"].Connections != 1 {
		t.Fatal("WS counts missing")
	}
	w := hubCall(h, "POST", "/console/api/transports?session="+a, `{"services":{"opq_ws":{"enabled":false,"port":0}}}`)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	_ = ca.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := ca.Read(make([]byte, 1)); err == nil {
		t.Fatal("disabled WS remained open")
	}
	if readTransports(t, h, a).Services["opq_ws"].Connections != 0 || readTransports(t, h, b).Services["opq_ws"].Connections != 1 {
		t.Fatal("disabling account changed another account")
	}
}

func TestTransportSettingsPersistByDeviceAcrossSessionIDs(t *testing.T) {
	h := testHub(t)
	h.options.TransportDir = t.TempDir()
	id, _ := h.Add("123456")
	port := freeProtocolPort(t)
	w := hubCall(h, "POST", "/console/api/transports?session="+id, fmt.Sprintf(`{"services":{"opq_ws":{"enabled":false,"port":0},"onebot_http":{"enabled":true,"port":%d}}}`, port))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if !readTransports(t, h, id).Persistent {
		t.Fatal("settings not persistent")
	}
	_ = h.Close()
	fresh := NewHub(h.options)
	defer fresh.Close()
	newID, err := fresh.Add("123456")
	if err != nil {
		t.Fatal(err)
	}
	if newID == id {
		t.Fatal("test did not change session ID")
	}
	s := readTransports(t, fresh, newID)
	if s.Services["opq_ws"].Enabled || s.Services["onebot_http"].Port != port || !s.Services["onebot_http"].Listening {
		t.Fatal("device settings lost across restart")
	}
	if w := hubCall(fresh, "POST", "/console/api/transports?session="+newID, `{"services":{"onebot_http":{"enabled":false,"port":0}}}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("disabled port was not released: %v", err)
	}
	_ = l.Close()
}

func TestConsoleRelayTestsDedicatedTransportAndRespectsDisabledGate(t *testing.T) {
	h := testHub(t)
	a, _ := h.Add("123456")
	b, _ := h.Add("234567")
	port := freeProtocolPort(t)
	if w := hubCall(h, "POST", "/console/api/transports?session="+a, fmt.Sprintf(`{"services":{"onebot_http":{"enabled":true,"port":%d}}}`, port)); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w := hubCall(h, "POST", "/console/api/relay?protocol=onebot&action=get_login_info&session="+a, `{}`)
	if !strings.Contains(w.Body.String(), `"user_id":123456`) {
		t.Fatalf("dedicated relay failed: %s", w.Body.String())
	}
	if w := hubCall(h, "POST", "/console/api/relay?protocol=onebot&action=get_login_info&qq=234567&session="+a, `{}`); !strings.Contains(w.Body.String(), `"user_id":123456`) {
		t.Fatal("relay rerouted to unselected account")
	}
	if w := hubCall(h, "POST", "/console/api/transports?session="+a, `{"services":{"onebot_http":{"enabled":false,"port":0}}}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := hubCall(h, "POST", "/console/api/relay?protocol=onebot&action=get_login_info&session="+a, `{}`); w.Code != 503 {
		t.Fatal("relay bypassed disabled service")
	}
	if w := hubCall(h, "POST", "/console/api/relay?protocol=onebot&action=get_login_info&session="+b, `{}`); !strings.Contains(w.Body.String(), `"user_id":234567`) {
		t.Fatal("relay disabled another account")
	}
	r := httptest.NewRequest("POST", "http://127.0.0.1:8090/console/api/relay?protocol=onebot&session="+b, strings.NewReader(`{}`))
	r.Header.Set("Origin", "https://example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != 403 {
		t.Fatal("cross-origin relay accepted")
	}
}

func TestManagedOneBotCanBeEnabledWithoutChangingLogin(t *testing.T) {
	h := testHub(t)
	h.options.API = api.Options{OPQ: true}
	id, _ := h.Add("123456")
	if readTransports(t, h, id).Services["onebot_http"].Enabled {
		t.Fatal("OneBot default changed")
	}
	if w := hubCall(h, "POST", "/console/api/transports?session="+id, `{"services":{"onebot_http":{"enabled":true,"port":0}}}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w := hubCall(h, "POST", "/accounts/"+id+"/onebot/v11/get_login_info", `{}`)
	if !strings.Contains(w.Body.String(), `"user_id":123456`) {
		t.Fatal("enabled transport has no adapter")
	}
}
