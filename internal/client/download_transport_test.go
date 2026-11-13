package client

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDownloadTransportRejectsResolvedMetadata(t *testing.T) {
	for _, ip := range []string{"169.254.169.254", "::ffff:169.254.169.254", "fe80::1", "fd00:ec2::254"} {
		t.Run(ip, func(t *testing.T) {
			tr := newDownloadTransport(func(context.Context, string) ([]net.IPAddr, error) {
				return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}, {IP: net.ParseIP(ip)}}, nil
			})
			defer tr.CloseIdleConnections()
			if tr.Proxy != nil {
				t.Fatal("environment proxy can bypass local DNS checks")
			}
			_, err := tr.DialContext(context.Background(), "tcp", "alias.example:80")
			if err == nil || !strings.Contains(err.Error(), "not allowed") {
				t.Fatalf("metadata address was not rejected: %v", err)
			}
		})
	}
}

func TestDownloadTransportPinsIPAndChecksRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Host, "safe.example:") {
			t.Errorf("Host header lost: %s", r.Host)
		}
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "http://metadata-alias.example/secret", http.StatusFound)
			return
		}
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	calls := 0
	tr := newDownloadTransport(func(_ context.Context, host string) ([]net.IPAddr, error) {
		calls++
		if host == "metadata-alias.example" {
			return []net.IPAddr{{IP: net.ParseIP("169.254.169.254")}}, nil
		}
		return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
	})
	defer tr.CloseIdleConnections()
	c := &http.Client{Transport: tr, Timeout: time.Second * 3}
	resp, err := c.Get("http://safe.example:" + port + "/ok")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if calls != 1 {
		t.Fatalf("unexpected DNS lookups: %d", calls)
	}
	_, err = c.Get("http://safe.example:" + port + "/redirect")
	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("redirect metadata was not blocked: %v", err)
	}
}
