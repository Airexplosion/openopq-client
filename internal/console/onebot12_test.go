package console

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func enable12(t *testing.T, h *Hub, id string) {
	t.Helper()
	w := hubCall(h, "POST", "/console/api/transports?session="+id, `{"services":{"onebot12_http":{"enabled":true,"port":0},"onebot12_ws":{"enabled":true,"port":0}}}`)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
}
func TestV12SharedRootSelfAndDedicatedStandardRoot(t *testing.T) {
	h := testHub(t)
	a, _ := h.Add("123456")
	b, _ := h.Add("234567")
	enable12(t, h, a)
	enable12(t, h, b)
	for _, v := range []struct{ id, qq string }{{a, "123456"}, {b, "234567"}} {
		w := hubCall(h, "POST", "/console/api/login?session="+v.id, `{"account":"`+v.qq+`"}`)
		if w.Code != 202 {
			t.Fatal(w.Body.String())
		}
		waitPhase(t, h.find(v.id).server, "online")
	}
	w := hubCall(h, "POST", "/onebot/v12/", `{"action":"get_self_info","params":{}}`) // Missing content type is a transport error.
	if w.Code != 415 {
		t.Fatal(w.Code)
	}
	call := func(path, body string) *httptest.ResponseRecorder {
		return secureCall(h, "POST", path, body, nil, map[string]string{"Content-Type": "application/json"})
	}
	if w = call("/onebot/v12/", `{"action":"get_self_info","params":{}}`); !strings.Contains(w.Body.String(), `"retcode":10101`) {
		t.Fatal(w.Body.String())
	}
	if w = call("/onebot/v12/", `{"action":"get_self_info","params":{},"self":{"platform":"qq","user_id":"234567"}}`); !strings.Contains(w.Body.String(), `"user_id":"234567"`) {
		t.Fatal(w.Body.String())
	}
	if w = call("/accounts/"+a+"/onebot/v12/", `{"action":"get_self_info","params":{},"self":{"platform":"qq","user_id":"234567"}}`); !strings.Contains(w.Body.String(), `"retcode":10102`) {
		t.Fatal("account-prefix escaped", w.Body.String())
	}
	port := freeProtocolPort(t)
	w = hubCall(h, "POST", "/console/api/transports?session="+a, fmt.Sprintf(`{"services":{"onebot12_http":{"enabled":true,"port":%d},"onebot12_ws":{"enabled":true,"port":%d}}}`, port, port))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	res, e := http.Post(fmt.Sprintf("http://127.0.0.1:%d/", port), "application/json", strings.NewReader(`{"action":"get_version","params":{}}`))
	if e != nil {
		t.Fatal(e)
	}
	defer res.Body.Close()
	var value map[string]any
	_ = json.NewDecoder(res.Body).Decode(&value)
	if res.StatusCode != 200 || value["status"] != "ok" {
		t.Fatal(value, res.StatusCode)
	}
}
func TestV12AuthPriorityWriteOnlySettingsAndRotation(t *testing.T) {
	h, key := secureTestHub(t)
	id, _ := h.Add("123456")
	cookies := loginConsole(t, h, key)
	ct := map[string]string{"Content-Type": "application/json"}
	patch := `{"services":{"onebot12_http":{"enabled":true,"port":0},"onebot12_ws":{"enabled":true,"port":0}}}`
	if w := secureCall(h, "POST", "/console/api/transports?session="+id, patch, cookies, ct); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	httpToken, wsToken := "v12-http-token-for-testing", "v12-ws-token-for-testing"
	settings := fmt.Sprintf(`{"http":{"enabled":true,"token":%q},"ws":{"enabled":true,"token":%q}}`, httpToken, wsToken)
	if w := secureCall(h, "POST", "/console/api/security", settings, cookies, ct); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	body := `{"action":"get_version","params":{}}`
	for _, v := range []struct {
		header, query string
		status        int
	}{{"", "", 401}, {"Bearer wrong", "", 401}, {"Bearer " + httpToken, "?access_token=wrong", 200}, {"", "?access_token=" + httpToken, 200}, {"bearer " + httpToken, "", 401}} {
		headers := map[string]string{"Content-Type": "application/json"}
		if v.header != "" {
			headers["Authorization"] = v.header
		}
		if w := secureCall(h, "POST", "/onebot/v12/"+v.query, body, nil, headers); w.Code != v.status {
			t.Fatal(v, w.Code, w.Body.String())
		}
	}
	token := "outbound-v12-test-secret"
	config := fmt.Sprintf(`{"event_buffer_size":20,"heartbeat_interval_ms":0,"webhooks":[{"url":"http://127.0.0.1:1/hook","access_token":%q,"timeout_ms":100}],"reverse_ws":[]}`, token)
	w := secureCall(h, "POST", "/console/api/onebot12?session="+id, config, cookies, ct)
	if w.Code != 200 || strings.Contains(w.Body.String(), token) || !strings.Contains(w.Body.String(), `"token_configured":true`) {
		t.Fatal(w.Body.String())
	}
	// Editing v11 settings must not erase the separately edited v12 settings.
	w = secureCall(h, "POST", "/console/api/onebot?session="+id, `{"message_format":"array"}`, cookies, ct)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = secureCall(h, "GET", "/console/api/onebot12?session="+id, "", cookies, nil)
	if !strings.Contains(w.Body.String(), `"token_configured":true`) {
		t.Fatal("v11 save erased v12")
	}
	server := httptest.NewServer(h)
	defer server.Close()
	header := http.Header{"Authorization": []string{"Bearer " + wsToken}}
	c, _, e := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/onebot/v12/", header)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	var event map[string]any
	if e = c.ReadJSON(&event); e != nil || event["detail_type"] != "connect" {
		t.Fatal(event, e)
	}
	newSettings := `{"ws":{"token":"v12-ws-replacement-testing"}}`
	if w = secureCall(h, "POST", "/console/api/security", newSettings, cookies, ct); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	for {
		if e = c.ReadJSON(&event); e != nil {
			break
		}
	}
}
