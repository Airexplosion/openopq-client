package sign

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSignReportsALostSessionAfterSignerRestart(t *testing.T) {
	for code, lost := range map[string]bool{
		"prepare_signer_session_first": true,
		"session_closed":               true,
		"account_mismatch":             false,
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			w.Write([]byte(`{"error":"` + code + `"}`))
		}))
		_, err := New(srv.URL+"/sign", time.Second).Sign(context.Background(), "test", 1, nil)
		srv.Close()
		if err == nil {
			t.Fatalf("%s: a refused signature was accepted", code)
		}
		if errors.Is(err, ErrSessionLost) != lost {
			t.Errorf("%s: ErrSessionLost = %v, want %v (%v)", code, !lost, lost, err)
		}
	}
}

// A resumable signer that has never seen this session (a gateway moved the client to
// another signer, or the checkpoint is gone) refuses the resume secret. Waiting for
// heartbeats to fail left accounts unsigned for six to nine minutes; the session is
// lost and has to be reported as such so the connection is rebuilt at once.
func TestSignReportsALostSessionWhenAResumableSignerRefusesToResume(t *testing.T) {
	known, resumes := true, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/capabilities":
			w.Write([]byte(`{"sessions":true,"resume":true}`))
		case r.URL.Path == "/session":
			w.Write([]byte(`{"session":"` + strings.Repeat("ab", 32) + `","resumeKey":"` + strings.Repeat("cd", 32) + `","epoch":"` + strings.Repeat("e", 32) + `"}`))
		case r.URL.Path == "/session/resume":
			resumes++
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"error":"resume_denied"}`))
		case known:
			w.Write([]byte(`{"sign":"aa","extra":"","deviceToken":""}`))
		default:
			w.WriteHeader(http.StatusConflict)
			w.Write([]byte(`{"error":"prepare_signer_session_first"}`))
		}
	}))
	defer srv.Close()
	client := New(srv.URL+"/sign", 2*time.Second)
	client.SetStatePath(filepath.Join(t.TempDir(), "state"))
	if err := client.Prepare(context.Background(), make([]byte, 16), 12345); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if _, err := client.Sign(context.Background(), "test", 1, nil); err != nil {
		t.Fatalf("sign on the signer that issued the session: %v", err)
	}
	// From here on the requests land on a signer that has no such session.
	known = false
	started := time.Now()
	_, err := client.Sign(context.Background(), "test", 2, nil)
	if !errors.Is(err, ErrSessionLost) {
		t.Fatalf("ErrSessionLost not reported: %v", err)
	}
	if resumes != 1 {
		t.Errorf("resume was tried %d times; a refused secret must not be retried", resumes)
	}
	if waited := time.Since(started); waited > time.Second {
		t.Errorf("took %s to report; it must not wait out the signing timeout", waited)
	}
}
