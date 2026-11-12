package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"openopq/internal/protocol"
)

func TestV11MediaMetadataContractWithoutDownloading(t *testing.T) {
	b := NewOneBotQueryBackend(nil)
	for _, kind := range []string{"image", "record"} {
		m := protocol.ReceivedMedia{Kind: kind, File: "observed." + kind, Name: "original." + kind, Size: 12345, URL: "https://gchat.qpic.cn/observed"}
		b.obMedia.observe(&protocol.IncomingMessage{Segments: []protocol.Segment{{Type: kind, Media: &m}}})
		raw, _ := json.Marshal(map[string]string{"file_id": m.File})
		out, err := b.OneBotAction(context.Background(), "get_"+kind, raw)
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]any{"file": m.URL, "url": m.URL, "file_size": "12345", "file_name": m.Name}
		if !reflect.DeepEqual(out, want) {
			t.Fatal(out)
		}
		if b.obMedia.root != "" {
			t.Fatal("metadata lookup created a local download cache")
		}
	}
	other := NewOneBotQueryBackend(nil)
	if _, err := other.OneBotAction(context.Background(), "get_image", json.RawMessage(`{"file":"observed.image"}`)); err == nil {
		t.Fatal("metadata crossed accounts")
	}
}

func TestV11ConvertedRecordKeepsSourceMetadata(t *testing.T) {
	b := NewOneBotQueryBackend(nil)
	m := protocol.ReceivedMedia{Kind: "record", File: "cached.record", Name: "sample.amr", Size: 12345, URL: "https://gchat.qpic.cn/record"}
	b.obMedia.observe(&protocol.IncomingMessage{Segments: []protocol.Segment{{Type: "record", Media: &m}}})
	b.obMedia.root = t.TempDir()
	// A previously converted fixture avoids external codecs and QQ networking.
	h := sha256.Sum256([]byte("record:" + m.File))
	prefix := filepath.Join(b.obMedia.root, hex.EncodeToString(h[:]))
	if err := os.WriteFile(prefix+".source", []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(prefix+".wav", []byte{0, 1, 2}, 0600); err != nil {
		t.Fatal(err)
	}
	out, err := b.OneBotAction(context.Background(), "get_record", json.RawMessage(`{"file":"cached.record","out_format":"wav"}`))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"file": m.URL, "url": m.URL, "file_name": m.Name, "file_size": "12345", "out_format": "wav", "base64": "AAEC"}
	if !reflect.DeepEqual(out, want) {
		t.Fatal(out)
	}
}

func TestReceivedMediaAccountAndPathIsolation(t *testing.T) {
	a, b := NewOneBotQueryBackend(nil), NewOneBotQueryBackend(nil)
	m := &protocol.IncomingMessage{Segments: []protocol.Segment{{Type: "image", Media: &protocol.ReceivedMedia{Kind: "image", File: "seen.image", URL: "https://gchat.qpic.cn/test"}}}}
	a.obMedia.observe(m)
	for _, raw := range []string{`{"file":"../../console-key.txt"}`, `{"file":"seen.image"}`} {
		if _, e := b.OneBotAction(context.Background(), "get_image", json.RawMessage(raw)); e == nil {
			t.Fatal("foreign or local file accepted")
		}
	}
	for _, raw := range []string{`{"file":"seen.image","out_format":"../mp3"}`, `{"file":"seen.image","out_format":"wav"}`} {
		if _, e := a.OneBotAction(context.Background(), "get_record", json.RawMessage(raw)); e == nil {
			t.Fatal("format or media-kind mismatch accepted")
		}
	}
	for _, s := range []string{"http://127.0.0.1/x", "file:///etc/passwd", "https://qpic.cn.evil.example/x", "https://user:pass@gchat.qpic.cn/x"} {
		u, _ := url.Parse(s)
		if validateMediaURL(u) == nil {
			t.Fatal("unsafe source allowed", s)
		}
	}
}
func TestModernMediaLocatorUsesAccountScene(t *testing.T) {
	for _, group := range []uint32{0, 90001} {
		b := NewOneBotQueryBackend(obQueryExchange(func(_ context.Context, cmd string, data []byte) ([]byte, error) {
			want := uint32(0x126d)
			if group != 0 {
				want = 0x126e
			}
			if cmd != protocol.OidbCommand(want, 200) {
				t.Fatal(cmd)
			}
			r, _ := protocol.UnwrapOIDB(data)
			if r.Reserved != 1 {
				t.Fatal("missing NT envelope flag")
			}
			f := obFields(r.Body)
			head := obFields(obBytes(f, 1))
			scene := obFields(obBytes(head, 2))
			if obNumber(scene, 102) != 3 {
				t.Fatal("voice treated as image")
			}
			if group == 0 {
				if obString(obFields(obBytes(scene, 201)), 2) != "u_self" {
					t.Fatal("incorrect account UID")
				}
			} else if obNumber(obFields(obBytes(scene, 202)), 1) != group {
				t.Fatal("incorrect group")
			}
			reply := protocol.Marshal(protocol.BytesField(3, protocol.Marshal(protocol.StringField(1, "&rkey=test"), protocol.BytesField(3, protocol.Marshal(protocol.StringField(1, "multimedia.nt.qq.com.cn"), protocol.StringField(2, "/download?x=1"))))))
			return obFixtureReply(cmd, reply), nil
		}))
		b.credentials.Uid = "u_self"
		got, e := b.mediaURL(context.Background(), protocol.ReceivedMedia{Kind: "record", Group: group, Index: protocol.Marshal(protocol.StringField(2, "uuid"))})
		if e != nil || got != "https://multimedia.nt.qq.com.cn/download?x=1&rkey=test" {
			t.Fatal(got, e)
		}
	}
}
