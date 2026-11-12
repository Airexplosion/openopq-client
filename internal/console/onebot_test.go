package console

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"openopq/internal/api"
)

func TestOneBotConfigAccountIsolationEncryptionAndRestore(t *testing.T) {
	h := testHub(t)
	h.options.TransportDir = t.TempDir()
	a, _ := h.Add("123456")
	b, _ := h.Add("234567")
	secret := "outbound-secret-for-test"
	body := fmt.Sprintf(`{"message_format":"string","heartbeat_interval_ms":0,"rate_limit_interval_ms":700,"http_post":[{"url":"http://127.0.0.1:1/events","secret":%q,"timeout_ms":100}],"reverse_ws":[]}`, secret)
	w := hubCall(h, "POST", "/console/api/onebot?session="+a, body)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if strings.Contains(w.Body.String(), secret) || !strings.Contains(w.Body.String(), `"secret_configured":true`) {
		t.Fatal("outbound credential exposed")
	}
	other := hubCall(h, "GET", "/console/api/onebot?session="+b, "")
	if strings.Contains(other.Body.String(), "127.0.0.1:1") {
		t.Fatal("account config leaked")
	}
	m := h.find(a).onebot
	raw, e := os.ReadFile(filepath.Join(m.directory(), m.guid+".enc"))
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(raw), secret) || json.Valid(raw) {
		t.Fatal("config stored as plaintext")
	}
	restored := newOneBotSettings(h, h.find(a))
	if e = restored.init(m.guid); e != nil {
		t.Fatal(e)
	}
	if restored.config.HTTPPost[0].Secret != secret || restored.config.RateLimitInterval != 700 {
		t.Fatal("encrypted config not restored")
	}
	bad := hubCall(h, "POST", "/console/api/onebot?session="+a, `{"message_format":"invalid"}`)
	if bad.Code != 400 {
		t.Fatal("invalid setting accepted")
	}
	saved, _ := h.find(a).server.api.(*api.Server).OneBotSnapshot()
	if saved.MessageFormat != "string" {
		t.Fatal("invalid request changed runtime")
	}
}
func TestOneBotSharedPortSelectionNeverFallsBackToWrongQQ(t *testing.T) {
	h := testHub(t)
	a, _ := h.Add("123456")
	b, _ := h.Add("234567")
	for _, p := range []string{"/get_login_info", "/get_login_info?self_id=99999", "/get_login_info?self_id=123456&qq=234567", "/get_login_info?self_id=123456&self_id=123456", "/accounts/" + a + "/get_login_info?self_id=234567"} {
		w := hubCall(h, "GET", p, "")
		if w.Code != 400 {
			t.Fatalf("ambiguous selector accepted %s: %s", p, w.Body.String())
		}
	}
	for _, v := range []struct{ url, uin string }{{"/get_login_info?self_id=123456", "123456"}, {"/accounts/" + b + "/get_login_info", "234567"}} {
		w := hubCall(h, "GET", v.url, "")
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"user_id":`+v.uin) {
			t.Fatalf("wrong account %s", w.Body.String())
		}
	}
}
