package client

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"openopq/internal/mediaconv"
	"openopq/internal/protocol"
)

type observedMedia struct {
	Media protocol.ReceivedMedia
	Seen  time.Time
}
type mediaRegistry struct {
	mu, op  sync.Mutex
	entries map[string]observedMedia
	root    string
}

func (r *mediaRegistry) observe(m *protocol.IncomingMessage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.entries == nil {
		r.entries = map[string]observedMedia{}
	}
	for k, v := range r.entries {
		if time.Since(v.Seen) > time.Hour {
			delete(r.entries, k)
		}
	}
	for _, s := range m.Segments {
		if s.Media == nil {
			continue
		}
		v := *s.Media
		if len(r.entries) >= 1024 {
			var oldest string
			var t time.Time
			for k, v := range r.entries {
				if oldest == "" || v.Seen.Before(t) {
					oldest, t = k, v.Seen
				}
			}
			delete(r.entries, oldest)
		}
		r.entries[v.File] = observedMedia{v, time.Now()}
	}
}
func (b *Bot) mediaURL(ctx context.Context, m protocol.ReceivedMedia) (string, error) {
	if len(m.Index) == 0 {
		if m.URL == "" {
			return "", fmt.Errorf("media has neither a download locator nor URL")
		}
		return m.URL, nil
	}
	requestType, business, requestID := uint64(2), uint64(1), uint64(1)
	service := uint32(0x11c5)
	if m.Kind == "video" {
		business, requestID, service = 2, 34, 0x11e9
	}
	if m.Kind == "record" {
		requestType, business = 1, 3
		service = 0x126d
	}
	sceneType := uint64(1)
	scene := []protocol.Field{}
	if m.Group != 0 {
		sceneType = 2
		if m.Kind == "record" {
			service, requestID = 0x126e, 4
		} else if m.Kind == "video" {
			service, requestID = 0x11e9, 1
		} else {
			service = 0x11c4
		}
		scene = append(scene, protocol.BytesField(202, protocol.Marshal(protocol.VarintField(1, uint64(m.Group)))))
	} else {
		uid := b.Credentials().Uid
		if uid == "" {
			return "", fmt.Errorf("current account UID is unavailable")
		}
		scene = append(scene, protocol.BytesField(201, protocol.Marshal(protocol.VarintField(1, 2), protocol.StringField(2, uid))))
	}
	scene = append(scene, protocol.VarintField(101, requestType), protocol.VarintField(102, business), protocol.VarintField(200, sceneType))
	head := protocol.Marshal(protocol.BytesField(1, protocol.Marshal(protocol.VarintField(1, requestID), protocol.VarintField(2, 200))), protocol.BytesField(2, protocol.Marshal(scene...)), protocol.BytesField(3, protocol.Marshal(protocol.VarintField(1, 2))))
	download := protocol.Marshal(protocol.BytesField(1, m.Index), protocol.BytesField(2, protocol.Marshal(protocol.BytesField(2, protocol.Marshal(protocol.VarintField(1, 0), protocol.VarintField(2, 0))))))
	f, e := b.obQuery(ctx, protocol.OidbCommand(service, 200), protocol.Marshal(protocol.BytesField(1, head), protocol.BytesField(3, download)), true)
	if e != nil {
		return "", e
	}
	rh := obFields(obBytes(f, 1))
	if obNumber(rh, 2) != 0 {
		return "", fmt.Errorf("QQ refused media download: %d %s", obNumber(rh, 2), obString(rh, 3))
	}
	d := obFields(obBytes(f, 3))
	info := obFields(obBytes(d, 3))
	domain, path, key := obString(info, 1), obString(info, 2), obString(d, 1)
	if domain == "" || !strings.HasPrefix(path, "/") {
		return "", fmt.Errorf("incomplete media URL")
	}
	return "https://" + domain + path + key, nil
}
func validateMediaURL(u *url.URL) error {
	if u.Scheme != "https" && u.Scheme != "http" || u.User != nil {
		return fmt.Errorf("invalid media URL")
	}
	host := strings.ToLower(u.Hostname())
	for _, domain := range []string{"qq.com", "qq.com.cn", "qpic.cn", "gtimg.cn", "gtimg.com"} {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return nil
		}
	}
	return fmt.Errorf("media URL is outside QQ media domains")
}
func downloadReceivedMedia(ctx context.Context, source, path string) error {
	u, e := url.Parse(source)
	if e != nil {
		return e
	}
	if e = validateMediaURL(u); e != nil {
		return e
	}
	client := &http.Client{Timeout: 45 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return fmt.Errorf("too many media redirects")
		}
		return validateMediaURL(req.URL)
	}}
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if e != nil {
		return e
	}
	resp, e := client.Do(req)
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("media download HTTP %d", resp.StatusCode)
	}
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	n, e := io.Copy(f, io.LimitReader(resp.Body, (30<<20)+1))
	closeErr := f.Close()
	if e == nil {
		e = closeErr
	}
	if n > 30<<20 {
		e = fmt.Errorf("media exceeds 30 MiB")
	}
	if e != nil {
		_ = os.Remove(path)
	}
	return e
}
func (b *Bot) mediaAction(ctx context.Context, action string, raw json.RawMessage) (any, error) {
	var p struct {
		File   string `json:"file"`
		FileID string `json:"file_id"`
		Format string `json:"out_format"`
	}
	if e := json.Unmarshal(raw, &p); e != nil {
		return nil, e
	}
	if p.File == "" {
		p.File = p.FileID
	}
	kind := "image"
	if action == "get_original_media" {
		b.obMedia.mu.Lock()
		v, ok := b.obMedia.entries[p.File]
		b.obMedia.mu.Unlock()
		if !ok {
			return nil, fmt.Errorf("unknown media for this account")
		}
		kind = v.Media.Kind
	}
	if action == "get_record" {
		kind = "record"
		if p.Format != "" && !mediaconv.ValidFormat(p.Format) {
			return nil, fmt.Errorf("out_format must be mp3, amr, wma, m4a, spx, ogg, wav or flac")
		}
	}
	r := &b.obMedia
	r.mu.Lock()
	v, ok := r.entries[p.File]
	r.mu.Unlock()
	if !ok || v.Media.Kind != kind || time.Since(v.Seen) > time.Hour {
		return nil, fmt.Errorf("unknown or expired media file for this account")
	}
	var wireInfo map[string]any
	var remoteURL string
	if action != "get_original_media" {
		var err error
		remoteURL, err = b.mediaURL(ctx, v.Media)
		if err != nil && kind != "image" {
			return nil, err
		}
		if remoteURL == "" {
			remoteURL = v.Media.URL
		}
		if remoteURL != "" {
			u, err := url.Parse(remoteURL)
			if err != nil {
				return nil, err
			}
			if err = validateMediaURL(u); err != nil {
				return nil, err
			}
		}
		file, name := remoteURL, v.Media.Name
		if file == "" {
			file = v.Media.File
		}
		if name == "" {
			name = v.Media.File
		}
		wireInfo = map[string]any{"file": file, "url": remoteURL, "file_size": strconv.FormatUint(uint64(v.Media.Size), 10), "file_name": name}
		if kind == "image" || p.Format == "" {
			return wireInfo, nil
		}
	}
	r.op.Lock()
	defer r.op.Unlock()
	if r.root == "" {
		var e error
		r.root, e = mediaconv.TempDir("account-")
		if e != nil {
			return nil, e
		}
	}
	// The cache is owned by this bot. User supplied filenames never become paths.
	entries, _ := os.ReadDir(r.root)
	var total int64
	for _, entry := range entries {
		info, e := entry.Info()
		if e != nil {
			continue
		}
		if time.Since(info.ModTime()) > time.Hour {
			_ = os.Remove(filepath.Join(r.root, entry.Name()))
		} else {
			total += info.Size()
		}
	}
	if total > 256<<20 {
		return nil, fmt.Errorf("account media cache is full; use clean_cache")
	}
	h := sha256.Sum256([]byte(kind + ":" + p.File))
	sourcePath := filepath.Join(r.root, hex.EncodeToString(h[:])+".source")
	if _, e := os.Stat(sourcePath); os.IsNotExist(e) {
		source := remoteURL
		if source == "" {
			var e error
			source, e = b.mediaURL(ctx, v.Media)
			if e != nil {
				return nil, e
			}
		}
		var e error
		if e = downloadReceivedMedia(ctx, source, sourcePath); e != nil {
			return nil, e
		}
	}
	if hash, _, ok := strings.Cut(p.File, "."); ok && len(hash) == 32 {
		data, e := os.ReadFile(sourcePath)
		if e != nil {
			return nil, e
		}
		actual := md5.Sum(data)
		if !strings.EqualFold(hash, hex.EncodeToString(actual[:])) {
			_ = os.Remove(sourcePath)
			return nil, fmt.Errorf("downloaded media hash differs from the received message")
		}
	}
	output := sourcePath
	if kind == "record" && action != "get_original_media" {
		output = filepath.Join(r.root, hex.EncodeToString(h[:])+"."+p.Format)
		if _, e := os.Stat(output); os.IsNotExist(e) {
			if e = mediaconv.Convert(ctx, sourcePath, output, p.Format); e != nil {
				return nil, e
			}
		}
	}
	info, e := os.Stat(output)
	if e != nil {
		return nil, e
	}
	if wireInfo != nil {
		if info.Size() > 30<<20 {
			return nil, fmt.Errorf("converted media exceeds 30 MiB")
		}
		data, err := os.ReadFile(output)
		if err != nil {
			return nil, err
		}
		wireInfo["out_format"], wireInfo["base64"] = p.Format, base64.StdEncoding.EncodeToString(data)
		return wireInfo, nil
	}
	return map[string]any{"file": output, "filename": v.Media.Name, "size": info.Size()}, nil
}
func (b *Bot) cleanMedia() error {
	r := &b.obMedia
	r.op.Lock()
	defer r.op.Unlock()
	if r.root != "" {
		if e := os.RemoveAll(r.root); e != nil {
			return e
		}
		r.root = ""
	}
	return nil
}

// GetOneBotFile exposes the original received file to OneBot v12's file registry.
// Account ownership and download checks remain inside the same media catalog.
func (b *Bot) GetOneBotFile(ctx context.Context, file string) (string, string, error) {
	raw, _ := json.Marshal(map[string]string{"file": file})
	v, e := b.mediaAction(ctx, "get_original_media", raw)
	if e != nil {
		return "", "", e
	}
	m := v.(map[string]any)
	return m["file"].(string), m["filename"].(string), nil
}
