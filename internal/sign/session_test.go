package sign

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSessionRoutesIdentitySignAndAccountTogether(t *testing.T) {
	token := strings.Repeat("a", 64)
	prepared, account, released := false, false, false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/capabilities":
			w.Write([]byte(`{"sessions":true}`))
		case "/session":
			if r.Method == "DELETE" {
				released = r.Header.Get("X-OPQ-Session") == token
				w.Write([]byte(`{}`))
				return
			}
			var v struct {
				GUID string `json:"guid"`
				Uin  uint32 `json:"uin"`
			}
			json.NewDecoder(r.Body).Decode(&v)
			prepared = v.GUID == strings.Repeat("00", 16) && v.Uin == 0
			json.NewEncoder(w).Encode(map[string]string{"session": token})
		case "/sign", "/account":
			if r.Header.Get("X-OPQ-Session") != token {
				w.WriteHeader(409)
				return
			}
			if r.URL.Path == "/account" {
				var v struct {
					Uin      uint32
					LoggedIn bool
				}
				json.NewDecoder(r.Body).Decode(&v)
				account = v.Uin == 123456 && v.LoggedIn
				w.Write([]byte(`{}`))
				return
			}
			w.Write([]byte(`{"sign":"01","extra":"","deviceToken":""}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	c := New(srv.URL+"/sign", time.Second)
	if err := c.Prepare(context.Background(), make([]byte, 16), 0); err != nil {
		t.Fatal(err)
	}
	if !prepared {
		t.Fatal("identity did not reach session initialization")
	}
	if _, err := c.Sign(context.Background(), "test", 1, []byte{1}); err != nil {
		t.Fatal(err)
	}
	if err := c.Account(context.Background(), 123456, true); err != nil {
		t.Fatal(err)
	}
	if !account {
		t.Fatal("authenticated account notification not routed")
	}
	if base, id := c.O3Session(); base != srv.URL || id != token {
		t.Fatal("O3 uses a different session")
	}
	if err := c.Release(); err != nil {
		t.Fatal(err)
	}
	if !released {
		t.Fatal("worker not released")
	}
	if _, id := c.O3Session(); id != "" {
		t.Fatal("closed session still attached")
	}
}
func TestLegacySignerNeedsNoSessionControl(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sign" {
			w.WriteHeader(404)
			return
		}
		if r.Header.Get("X-OPQ-Session") != "" {
			t.Error("legacy request gained session")
		}
		w.Write([]byte(`{"sign":"01"}`))
	}))
	defer srv.Close()
	c := New(srv.URL+"/sign", time.Second)
	if err := c.Prepare(context.Background(), make([]byte, 16), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Sign(context.Background(), "test", 1, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Account(context.Background(), 123456, true); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareTakesOverActiveIdentity(t *testing.T) {
	posts := 0
	token := strings.Repeat("ab", 32)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/capabilities":
			_, _ = w.Write([]byte(`{"sessions":true}`))
		case "/session":
			posts++
			if posts == 1 {
				w.WriteHeader(409)
				_, _ = w.Write([]byte(`{"error":"identity_already_active"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"session": token})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	if err := New(srv.URL+"/sign", time.Second).Prepare(context.Background(), make([]byte, 16), 0); err != nil {
		t.Fatal(err)
	}
	if posts != 2 {
		t.Fatalf("posts=%d, want 2", posts)
	}
}

func TestPrepareExplainsCapacityWithoutReflectingArbitraryServerText(t *testing.T) {
	for _, code := range []string{"two_distinct_guids_only_restart_for_new_device", "bad\nserver text"} {
		t.Run(code, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/capabilities" {
					_, _ = w.Write([]byte(`{"sessions":true}`))
					return
				}
				w.WriteHeader(http.StatusTooManyRequests)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
			}))
			defer srv.Close()
			err := New(srv.URL+"/sign", time.Second).Prepare(context.Background(), make([]byte, 16), 0)
			if err == nil || !strings.Contains(err.Error(), "HTTP 429") {
				t.Fatal("missing capacity failure")
			}
			if strings.Contains(err.Error(), code) != !strings.Contains(code, "\n") {
				t.Fatal("incorrect diagnostic filtering")
			}
		})
	}
}
