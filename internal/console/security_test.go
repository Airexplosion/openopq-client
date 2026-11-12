package console

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"openopq/internal/api"
)

func secureTestHub(t *testing.T) (*Hub, string) {
	t.Helper()
	h := testHub(t)
	dir := t.TempDir()
	s, err := LoadSecurity(dir)
	if err != nil {
		t.Fatal(err)
	}
	h.options.Security = s
	data, err := os.ReadFile(filepath.Join(dir, "console-key.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return h, strings.TrimSpace(string(data))
}
func secureCall(h *Hub, method, path, body string, cookies []*http.Cookie, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://127.0.0.1:8110"+path, strings.NewReader(body))
	for _, c := range cookies {
		r.AddCookie(c)
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func loginConsole(t *testing.T, h *Hub, key string) []*http.Cookie {
	t.Helper()
	data, _ := json.Marshal(map[string]string{"key": key})
	w := secureCall(h, "POST", "/console/api/auth/login", string(data), nil, nil)
	if w.Code != 200 {
		t.Fatalf("cannot login: %d", w.Code)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode || cookies[0].MaxAge != 43200 {
		t.Fatal("unsafe session cookie")
	}
	return cookies
}

func TestConsoleSecurityGuardsAllControlPathsAndLogout(t *testing.T) {
	h, key := secureTestHub(t)
	id, _ := h.Add("123456")
	for _, path := range []string{"/console/api/sessions", "/console/api/status", "/console/api/accounts", "/console/api/logs", "/console/api/security", "/console/api/signer", "/console/api/transports", "/console/api/diagnostics", "/accounts/" + id + "/console/api/status"} {
		if w := secureCall(h, "GET", path, ``, nil, nil); w.Code != 401 {
			t.Fatalf("unauthenticated control path %s: %d", path, w.Code)
		}
	}
	if w := secureCall(h, "GET", "/console/", ``, nil, nil); w.Code != 200 {
		t.Fatal("login assets unavailable")
	}
	if w := secureCall(h, "GET", "/console/api/auth/status", ``, nil, nil); !strings.Contains(w.Body.String(), `"authenticated":false`) {
		t.Fatal("anonymous considered authenticated")
	}
	cookies := loginConsole(t, h, key)
	if w := secureCall(h, "GET", "/console/api/sessions", ``, cookies, nil); w.Code != 200 {
		t.Fatal("session not authorized")
	}
	if w := secureCall(h, "POST", "/console/api/security", `{}`, cookies, map[string]string{"Origin": "https://evil.example"}); w.Code != 403 {
		t.Fatal("cross origin settings allowed")
	}
	if w := secureCall(h, "GET", "/console/api/security", ``, cookies, map[string]string{"Sec-Fetch-Site": "cross-site"}); w.Code != 403 {
		t.Fatal("cross site settings allowed")
	}
	if w := secureCall(h, "POST", "/console/api/auth/logout", `{}`, cookies, nil); w.Code != 200 {
		t.Fatal("logout failed")
	}
	if w := secureCall(h, "GET", "/console/api/sessions", ``, cookies, nil); w.Code != 401 {
		t.Fatal("logout left valid cookie")
	}
}

func TestSecurityTokenGatesSharedDedicatedAndAdminRelay(t *testing.T) {
	h, key := secureTestHub(t)
	id, _ := h.Add("123456")
	cookies := loginConsole(t, h, key)
	httpToken, wsToken := "test-http-unique-token-123456", "test-ws-unique-token-12345678"
	settings := fmt.Sprintf(`{"http":{"enabled":true,"token":%q},"ws":{"enabled":true,"token":%q}}`, httpToken, wsToken)
	w := secureCall(h, "POST", "/console/api/security", settings, cookies, nil)
	if w.Code != 200 {
		t.Fatalf("cannot set access policy: %s", w.Body.String())
	}
	for _, secret := range []string{httpToken, wsToken, key, "token_hash", "console_key_hash"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("secret leaked in snapshot")
		}
	}
	for _, path := range []string{"/v1/clusterinfo", "/v1/LuaApiCaller?qq=123456", "/accounts/" + id + "/onebot/v11/get_status", "/v1/login/getqrcode?qq=123456", "/ws?qq=123456", "/accounts/" + id + "/onebot/v11/ws"} {
		if w := secureCall(h, "GET", path, ``, cookies, nil); w.Code != 401 || w.Header().Get("WWW-Authenticate") == "" {
			t.Fatalf("cookie bypassed protocol auth %s", path)
		}
	}
	if w := secureCall(h, "POST", "/accounts/"+id+"/onebot/v11/get_login_info", `{}`, nil, map[string]string{"Authorization": "Bearer " + httpToken}); !strings.Contains(w.Body.String(), `"user_id":123456`) {
		t.Fatal("HTTP bearer rejected")
	}
	if w := secureCall(h, "POST", "/accounts/"+id+"/onebot/v11/get_status?access_token="+httpToken, `{}`, nil, nil); w.Code != 200 {
		t.Fatal("OneBot standard HTTP query token rejected")
	}
	for _, headers := range []map[string]string{nil, {"Authorization": "Bearer incorrect-token"}} {
		w := secureCall(h, "GET", "/accounts/"+id+"/onebot/v11/get_status", ``, nil, headers)
		var response map[string]any
		if w.Code != 401 || json.Unmarshal(w.Body.Bytes(), &response) != nil || len(response) != 4 || response["retcode"] != float64(1401) || response["wording"] != "unauthorized" || response["data"] != nil {
			t.Fatal("v11 auth envelope differs from action contract", w.Code, w.Body.String())
		}
	}
	if w := secureCall(h, "GET", "/v1/clusterinfo?access_token="+httpToken, ``, nil, nil); w.Code != 401 {
		t.Fatal("OPQ query token unexpectedly accepted")
	}
	if w := secureCall(h, "GET", "/v1/clusterinfo", ``, nil, map[string]string{"Authorization": "Bearer " + wsToken}); w.Code != 401 {
		t.Fatal("WS token accepted for HTTP")
	}
	if w := secureCall(h, "POST", "/console/api/relay?session="+id+"&protocol=onebot&action=get_login_info", `{}`, cookies, nil); !strings.Contains(w.Body.String(), `"user_id":123456`) {
		t.Fatal("admin relay unusable with auth enabled")
	}
	port := freeProtocolPort(t)
	if w := secureCall(h, "POST", "/console/api/transports?session="+id, fmt.Sprintf(`{"services":{"onebot_http":{"enabled":true,"port":%d}}}`, port), cookies, nil); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	client := &http.Client{Timeout: 2 * time.Second}
	for _, authorized := range []bool{false, true} {
		r, _ := http.NewRequest("POST", fmt.Sprintf("http://127.0.0.1:%d/onebot/v11/get_login_info", port), strings.NewReader(`{}`))
		if authorized {
			r.Header.Set("Authorization", "Bearer "+httpToken)
		}
		response, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if authorized && (!strings.Contains(string(data), `"user_id":123456`) || response.StatusCode != 200) {
			t.Fatal("dedicated valid bearer failed")
		}
		if !authorized && response.StatusCode != 401 {
			t.Fatal("dedicated endpoint bypassed token")
		}
	}
	// API credentials never unlock private management endpoints.
	if w := secureCall(h, "GET", "/console/api/sessions", ``, nil, map[string]string{"Authorization": "Bearer " + httpToken}); w.Code != 401 {
		t.Fatal("API bearer unlocked console")
	}
}

func TestCredentialRotationPersistenceAndLoginThrottle(t *testing.T) {
	h, key := secureTestHub(t)
	cookies := loginConsole(t, h, key)
	for i := 0; i < 5; i++ {
		if w := secureCall(h, "POST", "/console/api/auth/login", `{"key":"wrong"}`, nil, nil); w.Code != 401 {
			t.Fatalf("attempt %d status %d", i, w.Code)
		}
	}
	if w := secureCall(h, "POST", "/console/api/auth/login", `{"key":"wrong"}`, nil, nil); w.Code != 429 {
		t.Fatal("not throttled")
	}
	h.options.Security.mu.Lock()
	clear(h.options.Security.attempts)
	h.options.Security.mu.Unlock()
	for _, body := range []string{`{"http":{"enabled":true}}`, `{"ws":{"token":"short"}}`, fmt.Sprintf(`{"http":{"enabled":true,"token":%q}}`, key), `{"http":{"token":"identical-service-token"},"ws":{"token":"identical-service-token"}}`} {
		if w := secureCall(h, "POST", "/console/api/security", body, cookies, nil); w.Code != 400 {
			t.Fatal("invalid key policy accepted")
		}
	}
	newKey := "new-console-key-for-tests-123456"
	w := secureCall(h, "POST", "/console/api/security", fmt.Sprintf(`{"console_key":%q,"http":{"enabled":true,"token":"persistent-http-token-12345"}}`, newKey), cookies, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"reauthenticate":true`) {
		t.Fatal("console key rotation failed")
	}
	if w := secureCall(h, "GET", "/console/api/security", ``, cookies, nil); w.Code != 401 {
		t.Fatal("rotation retained old session")
	}
	if w := secureCall(h, "POST", "/console/api/auth/login", fmt.Sprintf(`{"key":%q}`, key), nil, nil); w.Code != 401 {
		t.Fatal("old console key accepted")
	}
	loginConsole(t, h, newKey)
	loaded, err := LoadSecurity(h.options.Security.dir)
	if err != nil {
		t.Fatal(err)
	}
	h.options.Security = loaded
	loginConsole(t, h, newKey)
	if w := secureCall(h, "GET", "/v1/clusterinfo", ``, nil, nil); w.Code != 401 {
		t.Fatal("HTTP policy lost on restart")
	}
	data, err := os.ReadFile(filepath.Join(loaded.dir, "security.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{key, newKey, "persistent-http-token-12345"} {
		if strings.Contains(string(data), secret) {
			t.Fatal("plaintext secret persisted in security.json")
		}
	}
	if _, err := os.Stat(filepath.Join(loaded.dir, "console-key.txt")); !os.IsNotExist(err) {
		t.Fatal("obsolete bootstrap key retained")
	}
}

func TestWSAuthenticationBeforeUpgradeRotationAndLogout(t *testing.T) {
	h, key := secureTestHub(t)
	id, _ := h.Add("123456")
	s := h.find(id).server
	s.api = api.NewServer(&transportEventBot{&testBot{}}, s.opt.Logger, api.Options{OPQ: true, OneBot: true})
	cookies := loginConsole(t, h, key)
	if w := secureCall(h, "POST", "/console/api/security", `{"ws":{"enabled":true,"token":"first-ws-access-token-123456"}}`, cookies, nil); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	server := httptest.NewServer(h)
	defer server.Close()
	open := func(path, header string, want int) net.Conn {
		c, err := net.DialTimeout("tcp", strings.TrimPrefix(server.URL, "http://"), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(c, "GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n%s\r\n", path, strings.TrimPrefix(server.URL, "http://"), header)
		_ = c.SetReadDeadline(time.Now().Add(time.Second))
		r, err := http.ReadResponse(bufio.NewReader(c), nil)
		if err != nil {
			t.Fatal(err)
		}
		if r.StatusCode != want {
			t.Fatalf("WS status=%d want=%d", r.StatusCode, want)
		}
		_ = c.SetReadDeadline(time.Time{})
		if want != 101 {
			c.Close()
			return nil
		}
		return c
	}
	base := "/accounts/" + id + "/ws"
	open(base, "", 401)
	open(base+"?access_token=first-ws-access-token-123456&access_token=first-ws-access-token-123456", "", 401)
	open(base+"?access_token=first-ws-access-token-123456", "Authorization: Bearer first-ws-access-token-123456\r\n", 401)
	if h.find(id).transports.snapshot("127.0.0.1", "123456").Services["opq_ws"].Connections != 0 {
		t.Fatal("failed upgrade counted connected")
	}
	public := open(base+"?access_token=first-ws-access-token-123456", "", 101)
	defer public.Close()
	admin := open("/console/api/socket?protocol=opq&session="+id, "Cookie: "+cookies[0].Name+"="+cookies[0].Value+"\r\n", 101)
	defer admin.Close()
	if w := secureCall(h, "POST", "/console/api/security", `{"ws":{"token":"second-ws-access-token-123456"}}`, cookies, nil); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	_ = public.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := public.Read(make([]byte, 1)); err == nil {
		t.Fatal("rotated token WS stayed connected")
	}
	if h.find(id).transports.snapshot("127.0.0.1", "123456").Services["opq_ws"].Connections != 1 {
		t.Fatal("token rotation closed admin test socket")
	}
	if w := secureCall(h, "POST", "/console/api/auth/logout", `{}`, cookies, nil); w.Code != 200 {
		t.Fatal("logout failed")
	}
	_ = admin.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := admin.Read(make([]byte, 1)); err == nil {
		t.Fatal("logout retained admin socket")
	}
}

func TestGatewayCredentialsAreNotForwardedToAttachedProcess(t *testing.T) {
	h, key := secureTestHub(t)
	var leaked bool
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/console/api/status" {
			send(w, 200, State{Account: "123456", GUID: strings.Repeat("ab", 16), Phase: "online", Online: true})
			return
		}
		leaked = r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.URL.Query().Has("access_token")
		send(w, 200, map[string]bool{"ok": true})
	}))
	defer old.Close()
	if err := h.Attach(old.URL); err != nil {
		t.Fatal(err)
	}
	cookies := loginConsole(t, h, key)
	if w := secureCall(h, "POST", "/console/api/security", `{"http":{"enabled":true,"token":"gateway-http-private-token-12345"}}`, cookies, nil); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w := secureCall(h, "POST", "/accounts/attached/onebot/v11/get_status", `{}`, cookies, map[string]string{"Authorization": "Bearer gateway-http-private-token-12345"})
	if w.Code != 200 || leaked {
		t.Fatal("credentials leaked to attached process")
	}
}
