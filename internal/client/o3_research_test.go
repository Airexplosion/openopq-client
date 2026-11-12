package client

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"openopq/internal/protocol"
)

type researchSigner struct {
	fakeSigner
	base     string
	accounts []bool
}

func (s *researchSigner) O3Session() (string, string) { return s.base, "managed-test-session" }
func (s *researchSigner) Account(_ context.Context, _ uint32, loggedIn bool) error {
	s.accounts = append(s.accounts, loggedIn)
	return nil
}

func TestNoO3ResearchPreservesAccountNotificationsAndLogin(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(`{"empty":true}`))
	}))
	defer server.Close()
	b, exchange := connectedBot(t)
	signer := &researchSigner{base: server.URL}
	b.options.Signer = signer
	b.options.DisableO3Relay = true
	b.login = nil
	if _, err := b.session(context.Background()); err != nil {
		t.Fatal(err)
	}
	exchange.key = b.login.shareKey
	exchange.replies = append(exchange.replies, loginScript(t, exchange.key, bytes.Repeat([]byte{0x5A}, 16))...)
	if _, err := b.QRCode(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := b.LoadSession(context.Background()); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 0 {
		t.Fatal("research opt-out forwarded O3 HTTP requests")
	}
	if len(signer.accounts) != 2 || signer.accounts[0] || !signer.accounts[1] {
		t.Fatalf("account lifecycle was skipped: %v", signer.accounts)
	}
	if !b.online {
		t.Fatal("scripted register did not complete")
	}
}

func TestO3RelayRemainsEnabledByDefault(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(`{"empty":true}`))
	}))
	defer server.Close()
	b := New(BotOptions{Signer: &researchSigner{base: server.URL}})
	b.o3Delegate(context.Background(), nil, time.Nanosecond, false)
	if requests.Load() == 0 {
		t.Fatal("default relay was disabled")
	}
}

type o3ReplyExchange struct{}

func (o3ReplyExchange) Exchange(context.Context, string, []byte) ([]byte, error) {
	return protocol.LV32([]byte{0x08, 0x01}), nil
}

func TestO3RelayDrainsBeyondEightRepliesAndStripsSSOLength(t *testing.T) {
	const total = 10
	var pulled, received atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/o3/trigger":
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/o3/pull":
			if pulled.Add(1) <= total {
				_, _ = fmt.Fprintf(w, `{"subseq":%d,"cmd":"trpc.o3.ecdh_access.EcdhAccess.SsoSecureA2Access","body":"0801"}`, pulled.Load())
				return
			}
			_, _ = w.Write([]byte(`{"empty":true}`))
		case "/o3/recv":
			var feed struct {
				Data string `json:"data"`
				RC   int    `json:"rc"`
			}
			if err := json.NewDecoder(r.Body).Decode(&feed); err != nil || feed.RC != 0 {
				t.Errorf("invalid O3 feed: %v rc=%d", err, feed.RC)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			body, err := base64.StdEncoding.DecodeString(feed.Data)
			if err != nil || !bytes.Equal(body, []byte{0x08, 0x01}) {
				t.Errorf("O3 feed retained bad framing: %x (%v)", body, err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			received.Add(1)
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	bot := New(BotOptions{Signer: &researchSigner{base: server.URL}})
	started := time.Now()
	bot.o3Delegate(context.Background(), o3ReplyExchange{}, 8*time.Second, true)
	if got := received.Load(); got != total {
		t.Fatalf("relayed %d replies, want all %d", got, total)
	}
	if time.Since(started) >= 7*time.Second {
		t.Fatal("SsoSecureA2Access replies did not end the quiet secure-stage wait")
	}
}

func TestO3RelayWorkerPollsOnlineSessionAndStopsOnClose(t *testing.T) {
	var pulls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/o3/pull" {
			pulls.Add(1)
		}
		_, _ = w.Write([]byte(`{"empty":true}`))
	}))
	defer server.Close()
	bot := New(BotOptions{Signer: &researchSigner{base: server.URL}})
	bot.online = true
	bot.startO3Relay(o3ReplyExchange{})
	deadline := time.Now().Add(4 * time.Second)
	for pulls.Load() < 4 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if pulls.Load() < 4 {
		t.Fatal("online O3 worker did not drain/poll the callback queue")
	}
	if err := bot.Close(); err != nil {
		t.Fatal(err)
	}
	afterClose := pulls.Load()
	time.Sleep(700 * time.Millisecond)
	if got := pulls.Load(); got != afterClose {
		t.Fatalf("O3 worker continued after close: %d -> %d", afterClose, got)
	}
}
