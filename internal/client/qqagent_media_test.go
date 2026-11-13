package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"openopq/internal/protocol"
)

func TestAgentMediaAliasesReuseAccountOwnedCache(t *testing.T) {
	b := NewOneBotQueryBackend(nil)
	b.obMedia.root = t.TempDir()
	b.obMedia.observe(&protocol.IncomingMessage{Segments: []protocol.Segment{{Media: &protocol.ReceivedMedia{Kind: "video", File: "video-key", Name: "video.mp4"}}}})
	h := sha256.Sum256([]byte("video:video-key"))
	file := filepath.Join(b.obMedia.root, hex.EncodeToString(h[:])+".source")
	if err := os.WriteFile(file, []byte("existing received video"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"get_video", "get_file"} {
		for _, param := range []string{`{"file":"video-key"}`, `{"file_id":"video-key"}`} {
			v, err := b.OneBotAction(context.Background(), action, json.RawMessage(param))
			if err != nil || v.(map[string]any)["file"] != file {
				t.Fatal(v, err)
			}
		}
		other := NewOneBotQueryBackend(nil)
		if _, err := other.OneBotAction(context.Background(), action, json.RawMessage(`{"file":"video-key"}`)); err == nil {
			t.Fatal("foreign account media accepted")
		}
		if _, err := b.OneBotAction(context.Background(), action, json.RawMessage(`{"file":"/etc/passwd"}`)); err == nil {
			t.Fatal("arbitrary local path accepted")
		}
	}
	if _, err := b.OneBotAction(context.Background(), "get_image", json.RawMessage(`{"file":"video-key"}`)); err == nil {
		t.Fatal("video treated as image")
	}
	b.obMedia.mu.Lock()
	v := b.obMedia.entries["video-key"]
	v.Seen = time.Now().Add(-2 * time.Hour)
	b.obMedia.entries["video-key"] = v
	b.obMedia.mu.Unlock()
	if _, err := b.OneBotAction(context.Background(), "get_file", json.RawMessage(`{"file":"video-key"}`)); err == nil {
		t.Fatal("expired media accepted")
	}
}

func TestGetFileReturnsOriginalImagesAndRecordsWithoutConversion(t *testing.T) {
	for _, kind := range []string{"image", "record"} {
		b := NewOneBotQueryBackend(nil)
		b.obMedia.root = t.TempDir()
		key := kind + "-key"
		b.obMedia.observe(&protocol.IncomingMessage{Segments: []protocol.Segment{{Media: &protocol.ReceivedMedia{Kind: kind, File: key}}}})
		h := sha256.Sum256([]byte(kind + ":" + key))
		file := filepath.Join(b.obMedia.root, hex.EncodeToString(h[:])+".source")
		if err := os.WriteFile(file, []byte("already received media"), 0600); err != nil {
			t.Fatal(err)
		}
		out, err := b.OneBotAction(context.Background(), "get_file", json.RawMessage(`{"file_id":"`+key+`"}`))
		if err != nil || out.(map[string]any)["file"] != file {
			t.Fatal(kind, out, err)
		}
		if _, err := b.OneBotAction(context.Background(), "get_video", json.RawMessage(`{"file":"`+key+`"}`)); err == nil {
			t.Fatal("get_video accepted", kind)
		}
	}
}
