package sign

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLostRepliesAndHostRestartRecoverWithoutRepeatingNativeWork(t *testing.T) {
	var mu sync.Mutex
	epoch, token := strings.Repeat("1", 32), strings.Repeat("a", 64)
	secret := strings.Repeat("b", 64)
	cache := map[string][]byte{}
	acks := map[string]bool{}
	signs, feeds, resumes := 0, 0, 0
	loseSign, loseFeed := true, true
	recovering := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("X-OPQ-Epoch", epoch)
		if r.URL.Path == "/capabilities" {
			io.WriteString(w, `{"sessions":true,"resume":true}`)
			return
		}
		if r.URL.Path == "/session" {
			json.NewEncoder(w).Encode(map[string]any{"session": token, "resumeKey": secret, "epoch": epoch})
			return
		}
		if r.URL.Path == "/session/resume" {
			var v struct{ ResumeKey string }
			json.NewDecoder(r.Body).Decode(&v)
			if v.ResumeKey != secret {
				w.WriteHeader(403)
				return
			}
			resumes++
			json.NewEncoder(w).Encode(map[string]any{"session": token, "epoch": epoch, "recoveryRequired": recovering})
			return
		}
		if r.Header.Get("X-OPQ-Session") != token {
			w.WriteHeader(409)
			io.WriteString(w, `{"error":"prepare_signer_session_first"}`)
			return
		}
		switch r.URL.Path {
		case "/sign":
			if recovering {
				w.WriteHeader(503)
				io.WriteString(w, `{"error":"signer_recovering"}`)
				return
			}
			id := r.Header.Get("X-OPQ-Request-ID")
			if id == "" {
				t.Error("request lost idempotency identity")
			}
			value, ok := cache[id]
			if !ok {
				signs++
				value = []byte(`{"sign":"0102","extra":"","deviceToken":""}`)
				cache[id] = value
			}
			if loseSign {
				loseSign = false
				conn, _, _ := w.(http.Hijacker).Hijack()
				conn.Close()
				return
			}
			w.Write(value)
		case "/o3/pull":
			io.WriteString(w, `{"cmd":"trpc.o3.ecdh_access.EcdhAccess.SsoSecureA2Access","subseq":42,"body":"0801"}`)
		case "/o3/recv":
			if r.Header.Get("X-OPQ-Epoch") != epoch {
				w.WriteHeader(409)
				io.WriteString(w, `{"error":"signer_epoch_changed"}`)
				return
			}
			var v struct{ Subseq json.RawMessage }
			json.NewDecoder(r.Body).Decode(&v)
			id := string(v.Subseq)
			if !acks[id] {
				feeds++
				acks[id] = true
			}
			recovering = false
			if loseFeed {
				loseFeed = false
				conn, _, _ := w.(http.Hijacker).Hijack()
				conn.Close()
				return
			}
			io.WriteString(w, `{"ok":true}`)
		case "/account":
			io.WriteString(w, `{}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	c := New(server.URL+"/sign", 3*time.Second)
	state := filepath.Join(t.TempDir(), "session.enc")
	c.SetStatePath(state)
	if err := c.Prepare(context.Background(), make([]byte, 16), 12345); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Sign(context.Background(), "command", 1, []byte{1}); err != nil {
		t.Fatal(err)
	}
	response, err := c.DoO3(context.Background(), "GET", "/o3/pull", nil)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	response, err = c.DoO3(context.Background(), "POST", "/o3/recv", []byte(`{"cmd":"trpc.o3.ecdh_access.EcdhAccess.SsoSecureA2Access","subseq":42,"rc":0,"data":"AQID"}`))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	mu.Lock()
	if signs != 1 || feeds != 1 {
		t.Fatalf("duplicate native work: %d %d", signs, feeds)
	}
	epoch = strings.Repeat("2", 32)
	token = strings.Repeat("c", 64)
	recovering = true
	cache = map[string][]byte{}
	acks = map[string]bool{}
	mu.Unlock()
	done := make(chan error, 1)
	go func() { _, err := c.Sign(context.Background(), "command", 2, []byte{2}); done <- err }()
	time.Sleep(200 * time.Millisecond)
	response, err = c.DoO3(context.Background(), "GET", "/o3/pull", nil)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	response, err = c.DoO3(context.Background(), "POST", "/o3/recv", []byte(`{"cmd":"trpc.o3.ecdh_access.EcdhAccess.SsoSecureA2Access","subseq":42,"rc":0,"data":"AQID"}`))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("business signing blocked O3 recovery")
	}
	mu.Lock()
	if resumes != 1 || signs != 2 || feeds != 2 {
		t.Fatalf("unexpected recovery counts: resume=%d sign=%d feed=%d", resumes, signs, feeds)
	}
	mu.Unlock()
	data, err := os.ReadFile(state)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secret) {
		t.Fatal("resume credential stored unencrypted")
	}
	clone := New(server.URL+"/sign", time.Second)
	clone.SetStatePath(state)
	if err := clone.Prepare(context.Background(), make([]byte, 16), 12345); err != nil {
		t.Fatal(err)
	}
	data[20] ^= 1
	os.WriteFile(state, data, 0600)
	bad := New(server.URL+"/sign", time.Second)
	bad.SetStatePath(state)
	if err := bad.Prepare(context.Background(), make([]byte, 16), 12345); err == nil {
		t.Fatal("corrupt checkpoint accepted")
	}
}

func TestOldEpochPendingO3ReplyIsDiscardedInsteadOfFedIntoNewSDK(t *testing.T) {
	var mu sync.Mutex
	token := strings.Repeat("a", 64)
	epoch := strings.Repeat("1", 32)
	feeds := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("X-OPQ-Epoch", epoch)
		switch r.URL.Path {
		case "/capabilities":
			io.WriteString(w, `{"sessions":true,"resume":true}`)
		case "/session", "/session/resume":
			json.NewEncoder(w).Encode(map[string]string{"session": token, "resumeKey": strings.Repeat("b", 64), "epoch": epoch})
		case "/o3/recv":
			if r.Header.Get("X-OPQ-Session") != token {
				w.WriteHeader(409)
				io.WriteString(w, `{"error":"prepare_signer_session_first"}`)
				return
			}
			feeds++
			io.WriteString(w, `{"ok":true}`)
		case "/o3/pull":
			io.WriteString(w, `{"empty":true}`)
		}
	}))
	defer server.Close()
	c := New(server.URL+"/sign", time.Second)
	if err := c.Prepare(context.Background(), make([]byte, 16), 0); err != nil {
		t.Fatal(err)
	}
	c.pending = &pendingReply{Epoch: epoch, Body: []byte(`{"subseq":7}`)}
	mu.Lock()
	epoch = strings.Repeat("2", 32)
	token = strings.Repeat("c", 64)
	mu.Unlock()
	response, err := c.DoO3(context.Background(), "GET", "/o3/pull", nil)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	mu.Lock()
	defer mu.Unlock()
	if feeds != 0 || c.pending != nil {
		t.Fatal("old reply was fed into new SDK")
	}
}
