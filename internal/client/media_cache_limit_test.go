package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDownloadCacheConfiguredBoundary(t *testing.T) {
	for _, size := range []int{4, 5} {
		t.Run(string(rune('0'+size)), func(t *testing.T) {
			b := New(BotOptions{MediaCacheMiB: 1})
			b.obMedia.root = t.TempDir()
			f, err := os.Create(filepath.Join(b.obMedia.root, "existing"))
			if err != nil {
				t.Fatal(err)
			}
			if err = f.Truncate((1 << 20) - 4); err != nil {
				f.Close()
				t.Fatal(err)
			}
			f.Close()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(make([]byte, size)) }))
			defer srv.Close()
			raw, _ := json.Marshal(map[string]string{"url": srv.URL + "/test"})
			_, err = b.downloadNamedFile(context.Background(), raw)
			if (err != nil) != (size > 4) {
				t.Fatalf("size=%d err=%v", size, err)
			}
			entries, err := os.ReadDir(b.obMedia.root)
			if err != nil {
				t.Fatal(err)
			}
			want := 2
			if size > 4 {
				want = 1
			}
			if len(entries) != want {
				t.Fatalf("partial download retained: %d files", len(entries))
			}
		})
	}
}

func TestDownloadCacheCanBeRaised(t *testing.T) {
	b := New(BotOptions{MediaCacheMiB: 1024})
	b.obMedia.root = t.TempDir()
	f, err := os.Create(filepath.Join(b.obMedia.root, "existing"))
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Truncate(257 << 20); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }))
	defer srv.Close()
	raw, _ := json.Marshal(map[string]string{"url": srv.URL + "/test"})
	if _, err := b.downloadNamedFile(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
}

func TestReviewDownloadHonorsCacheLimit(t *testing.T) {
	b := &Bot{}
	b.obMedia.root = t.TempDir()
	f, err := os.Create(filepath.Join(b.obMedia.root, "existing"))
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Truncate(257 << 20); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("test")) }))
	defer srv.Close()
	raw, _ := json.Marshal(map[string]string{"url": srv.URL + "/test"})
	if _, err = b.downloadNamedFile(context.Background(), raw); err == nil {
		t.Fatal("download accepted with account cache already above 256 MiB")
	}
}

func TestReviewDownloadExpiresOldFiles(t *testing.T) {
	b := &Bot{}
	b.obMedia.root = t.TempDir()
	old := filepath.Join(b.obMedia.root, "expired")
	if err := os.WriteFile(old, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(old, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("test")) }))
	defer srv.Close()
	raw, _ := json.Marshal(map[string]string{"url": srv.URL + "/test"})
	if _, err := b.downloadNamedFile(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); err == nil {
		t.Fatal("download leaves expired account cache files untouched")
	}
}
