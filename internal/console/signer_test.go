package console

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestSignerHealthRetainsPrefixAndAuthentication(t *testing.T) {
	called := false
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		user, key, ok := r.BasicAuth()
		if r.URL.Path != "/service/health" || !ok || user != "client" || key != "test-private-key" {
			t.Error("wrong path or authentication")
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ready":true,"adapter":"test"}`))
	}))
	defer remote.Close()
	endpoint, _ := url.Parse(remote.URL + "/service/sign")
	endpoint.User = url.UserPassword("client", "test-private-key")
	s := &Server{opt: Options{Signer: endpoint.String()}}
	response := httptest.NewRecorder()
	s.signer(response, httptest.NewRequest("GET", "/console/api/signer", nil))
	if !called || !strings.Contains(response.Body.String(), `"ready":true`) {
		t.Fatal(response.Body.String())
	}
	if strings.Contains(response.Body.String(), "test-private-key") {
		t.Fatal("credential leaked")
	}
}
