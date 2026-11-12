package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"openopq/internal/mediaconv"
)

const maxFile12 = int64(30 << 20)

type range12 struct{ start, end int64 }
type file12 struct {
	ID, Name, Path, URL, Remote, Kind, SHA string
	Size                                   int64
	Ready, Owned, Busy                     bool
	Created                                time.Time
	Ranges                                 []range12
}
type fileStore12 struct {
	op, mu  sync.Mutex
	root    string
	entries map[string]file12
	aliases map[string]string
	closed  bool
}

func (f *fileStore12) put(e file12) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.entries == nil {
		f.entries = map[string]file12{}
	}
	f.entries[e.ID] = e
}
func (f *fileStore12) begin(id string) (file12, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.entries[id]
	if !ok || time.Since(e.Created) > time.Hour {
		return e, bad12(35000, "unknown or expired file_id for this account")
	}
	e.Busy = true
	f.entries[id] = e
	return e, nil
}
func (f *fileStore12) end(id string) {
	f.mu.Lock()
	e, ok := f.entries[id]
	if ok {
		e.Busy = false
		f.entries[id] = e
	}
	f.mu.Unlock()
}
func (f *fileStore12) pruneLocked() {
	for id, e := range f.entries {
		if !e.Busy && time.Since(e.Created) > time.Hour {
			if e.Owned && e.Path != "" {
				_ = os.Remove(e.Path)
			}
			delete(f.entries, id)
		}
	}
	for len(f.entries) >= 1024 {
		old := ""
		var t time.Time
		for id, e := range f.entries {
			if !e.Busy && (old == "" || e.Created.Before(t)) {
				old, t = id, e.Created
			}
		}
		if old == "" {
			break
		}
		e := f.entries[old]
		if e.Owned && e.Path != "" {
			_ = os.Remove(e.Path)
		}
		delete(f.entries, old)
	}
	for key, id := range f.aliases {
		if _, ok := f.entries[id]; !ok {
			delete(f.aliases, key)
		}
	}
}
func (f *fileStore12) remote(kind, name, source string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return ""
	}
	if f.entries == nil {
		f.entries = map[string]file12{}
		f.aliases = map[string]string{}
	}
	key := kind + ":" + name
	if id, ok := f.aliases[key]; ok {
		if e, ok := f.entries[id]; ok && time.Since(e.Created) < time.Hour {
			return id
		}
	}
	f.pruneLocked()
	id := id12()
	f.entries[id] = file12{ID: id, Name: name, Remote: name, Kind: kind, URL: source, Created: time.Now()}
	f.aliases[key] = id
	return id
}
func (f *fileStore12) newFile(name string, size int64) (file12, error) {
	if f.root == "" {
		var e error
		f.root, e = mediaconv.TempDir("onebot12-")
		if e != nil {
			return file12{}, e
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return file12{}, bad12(35000, "file store closed")
	}
	if f.entries == nil {
		f.entries = map[string]file12{}
		f.aliases = map[string]string{}
	}
	f.pruneLocked()
	var total int64
	for _, e := range f.entries {
		total += e.Size
	}
	if total+size > 256<<20 {
		return file12{}, bad12(32000, "account file cache exceeds 256 MiB")
	}
	id := id12()
	return file12{ID: id, Name: name, Size: size, Owned: true, Path: filepath.Join(f.root, id), Created: time.Now()}, nil
}
func (f *fileStore12) close() {
	f.op.Lock()
	defer f.op.Unlock()
	f.mu.Lock()
	f.closed = true
	f.entries = nil
	f.aliases = nil
	f.mu.Unlock()
	if f.root != "" {
		_ = os.RemoveAll(f.root)
		f.root = ""
	}
}
func boundedFile12(path string) ([]byte, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil {
		return nil, e
	}
	if !st.Mode().IsRegular() || st.Size() > maxFile12 {
		return nil, bad12(10004, "file must be regular and at most 30 MiB")
	}
	b, e := io.ReadAll(io.LimitReader(f, maxFile12+1))
	if len(b) > int(maxFile12) {
		return nil, bad12(10004, "file exceeds 30 MiB")
	}
	return b, e
}
func (o *oneBot12Runtime) loadFile(ctx context.Context, e *file12) error {
	if e.Ready {
		if _, err := os.Stat(e.Path); err == nil {
			return nil
		}
		if e.Remote == "" {
			return bad12(32000, "cached file is no longer available")
		}
		e.Ready = false
	}
	if e.Remote == "" {
		return bad12(35000, "fragmented upload is not finished")
	}
	if src, ok := o.s.bot.(interface {
		GetOneBotFile(context.Context, string) (string, string, error)
	}); ok {
		path, name, err := src.GetOneBotFile(ctx, e.Remote)
		if err != nil {
			return err
		}
		e.Path, e.Name = path, name
		if e.Name == "" {
			e.Name = e.Remote
		}
		e.Owned = false
	} else {
		action := "get_image"
		params := map[string]any{"file": e.Remote}
		if e.Kind == "record" {
			action = "get_record"
			params["out_format"] = "wav"
		}
		v, err := o.s.call11For12(ctx, action, params)
		if err != nil {
			return err
		}
		m := map12(v)
		e.Path, _ = m["file"].(string)
	}
	data, err := boundedFile12(e.Path)
	if err != nil {
		return err
	}
	h := sha256.Sum256(data)
	e.SHA, e.Size, e.Ready = hex.EncodeToString(h[:]), int64(len(data)), true
	o.files.put(*e)
	return nil
}
func (o *oneBot12Runtime) resolveFile(ctx context.Context, id string) (string, error) {
	f := &o.files
	f.op.Lock()
	defer f.op.Unlock()
	e, err := f.begin(id)
	if err != nil {
		return "", err
	}
	defer f.end(id)
	if err = o.loadFile(ctx, &e); err != nil {
		return "", err
	}
	return e.Path, nil
}
func intParam12(p map[string]json.RawMessage, k string, required bool) (int64, error) {
	raw, ok := p[k]
	if !ok && !required {
		return 0, nil
	}
	var n int64
	if !ok || string(raw) == "null" || json.Unmarshal(raw, &n) != nil {
		return 0, bad12(10003, k+" must be an integer")
	}
	return n, nil
}
func hash12(data []byte, expected string, required bool) (string, error) {
	h := sha256.Sum256(data)
	actual := hex.EncodeToString(h[:])
	if required || expected != "" {
		b, e := hex.DecodeString(expected)
		if e != nil || len(b) != 32 || expected != strings.ToLower(expected) {
			return "", bad12(10003, "sha256 must be 64 lowercase hex characters")
		}
		if actual != expected {
			return "", bad12(35000, "file SHA256 mismatch")
		}
	}
	return actual, nil
}
func (o *oneBot12Runtime) fileAction(ctx context.Context, action string, p map[string]json.RawMessage) (any, error) {
	f := &o.files
	f.op.Lock()
	defer f.op.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	stop := context.AfterFunc(o.ctx, cancel)
	defer stop()
	if action == "upload_file" {
		kind, e := stringParam12(p, "type", true)
		if e != nil {
			return nil, e
		}
		name, e := stringParam12(p, "name", true)
		if e != nil {
			return nil, e
		}
		if len(name) > 256 {
			return nil, bad12(10003, "name is too long")
		}
		var data []byte
		switch kind {
		case "data":
			if _, ok := p["data"]; !ok || json.Unmarshal(p["data"], &data) != nil || string(p["data"]) == "null" {
				return nil, bad12(10003, "data must be Base64 bytes")
			}
		case "path":
			path, e := stringParam12(p, "path", true)
			if e != nil {
				return nil, e
			}
			data, e = boundedFile12(path)
			if e != nil {
				return nil, e
			}
		case "url":
			source, e := stringParam12(p, "url", true)
			if e != nil {
				return nil, e
			}
			u, e := url.Parse(source)
			if e != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
				return nil, bad12(10003, "url must use HTTP(S)")
			}
			req, e := http.NewRequestWithContext(ctx, "GET", source, nil)
			if e != nil {
				return nil, e
			}
			var headers map[string]string
			if raw, ok := p["headers"]; ok {
				if json.Unmarshal(raw, &headers) != nil {
					return nil, bad12(10003, "headers must be a string map")
				}
			}
			for k, v := range headers {
				if strings.EqualFold(k, "Host") {
					req.Host = v
				} else {
					req.Header.Set(k, v)
				}
			}
			client := &http.Client{CheckRedirect: func(r *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return fmt.Errorf("too many redirects")
				}
				if r.URL.Scheme != "http" && r.URL.Scheme != "https" {
					return fmt.Errorf("invalid redirect")
				}
				if len(via) > 0 && r.URL.Host != via[0].URL.Host {
					r.Header = http.Header{}
				}
				return nil
			}}
			res, e := client.Do(req)
			if e != nil {
				return nil, bad12(33000, e.Error())
			}
			defer res.Body.Close()
			if res.StatusCode != 200 {
				return nil, bad12(33000, fmt.Sprintf("download HTTP %d", res.StatusCode))
			}
			data, e = io.ReadAll(io.LimitReader(res.Body, maxFile12+1))
			if e != nil {
				return nil, e
			}
		default:
			return nil, bad12(10004, "unsupported upload type")
		}
		if int64(len(data)) > maxFile12 {
			return nil, bad12(10004, "file exceeds 30 MiB")
		}
		expected, e := stringParam12(p, "sha256", false)
		if e != nil {
			return nil, e
		}
		hash, e := hash12(data, expected, false)
		if e != nil {
			return nil, e
		}
		file, e := f.newFile(name, int64(len(data)))
		if e != nil {
			return nil, e
		}
		if e = os.WriteFile(file.Path, data, 0600); e != nil {
			return nil, e
		}
		file.Ready, file.SHA = true, hash
		f.put(file)
		return map[string]any{"file_id": file.ID}, nil
	}
	stage, _ := stringParam12(p, "stage", false)
	if action == "upload_file_fragmented" && stage == "prepare" {
		name, e := stringParam12(p, "name", true)
		if e != nil {
			return nil, e
		}
		size, e := intParam12(p, "total_size", true)
		if e != nil {
			return nil, e
		}
		if size < 0 || size > maxFile12 {
			return nil, bad12(10004, "total_size must be 0..30 MiB")
		}
		file, e := f.newFile(name, size)
		if e != nil {
			return nil, e
		}
		fd, e := os.OpenFile(file.Path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
		if e != nil {
			return nil, e
		}
		e = fd.Truncate(size)
		_ = fd.Close()
		if e != nil {
			_ = os.Remove(file.Path)
			return nil, e
		}
		f.put(file)
		return map[string]any{"file_id": file.ID}, nil
	}
	id, e := stringParam12(p, "file_id", true)
	if e != nil {
		return nil, e
	}
	entry, e := f.begin(id)
	if e != nil {
		return nil, e
	}
	defer f.end(id)
	if action == "upload_file_fragmented" {
		if entry.Ready || !entry.Owned {
			return nil, bad12(35000, "not an unfinished upload")
		}
		switch stage {
		case "transfer":
			offset, e := intParam12(p, "offset", true)
			if e != nil {
				return nil, e
			}
			var data []byte
			if json.Unmarshal(p["data"], &data) != nil || string(p["data"]) == "null" {
				return nil, bad12(10003, "data must be Base64 bytes")
			}
			if offset < 0 || offset > entry.Size || int64(len(data)) > entry.Size-offset {
				return nil, bad12(10003, "fragment outside file bounds")
			}
			if len(entry.Ranges) >= 8192 {
				return nil, bad12(35000, "too many fragments")
			}
			fd, e := os.OpenFile(entry.Path, os.O_RDWR, 0600)
			if e != nil {
				return nil, e
			}
			defer fd.Close()
			for _, r := range entry.Ranges {
				start, end := offset, offset+int64(len(data))
				if start < r.start {
					start = r.start
				}
				if end > r.end {
					end = r.end
				}
				if start < end {
					prior := make([]byte, end-start)
					_, e = fd.ReadAt(prior, start)
					if e != nil {
						return nil, e
					}
					for i, b := range prior {
						if b != data[start-offset+int64(i)] {
							return nil, bad12(35000, "overlapping fragment contains conflicting bytes")
						}
					}
				}
			}
			if _, e = fd.WriteAt(data, offset); e != nil {
				return nil, e
			}
			entry.Ranges = append(entry.Ranges, range12{offset, offset + int64(len(data))})
			sort.Slice(entry.Ranges, func(i, j int) bool { return entry.Ranges[i].start < entry.Ranges[j].start })
			merged := []range12{}
			for _, r := range entry.Ranges {
				if len(merged) > 0 && r.start <= merged[len(merged)-1].end {
					if r.end > merged[len(merged)-1].end {
						merged[len(merged)-1].end = r.end
					}
				} else {
					merged = append(merged, r)
				}
			}
			entry.Ranges = merged
			f.put(entry)
			return nil, nil
		case "finish":
			if entry.Size > 0 && (len(entry.Ranges) != 1 || entry.Ranges[0].start != 0 || entry.Ranges[0].end != entry.Size) {
				return nil, bad12(35000, "upload has missing fragments")
			}
			data, e := boundedFile12(entry.Path)
			if e != nil {
				return nil, e
			}
			expected, e := stringParam12(p, "sha256", true)
			if e != nil {
				return nil, e
			}
			hash, e := hash12(data, expected, true)
			if e != nil {
				return nil, e
			}
			entry.Ready, entry.SHA = true, hash
			entry.Ranges = nil
			f.put(entry)
			return map[string]any{"file_id": entry.ID}, nil
		default:
			return nil, bad12(10003, "stage must be prepare, transfer or finish")
		}
	}
	if e = o.loadFile(ctx, &entry); e != nil {
		return nil, e
	}
	if action == "get_file_fragmented" {
		switch stage {
		case "prepare":
			return map[string]any{"name": entry.Name, "total_size": entry.Size, "sha256": entry.SHA}, nil
		case "transfer":
			offset, e := intParam12(p, "offset", true)
			if e != nil {
				return nil, e
			}
			size, e := intParam12(p, "size", true)
			if e != nil {
				return nil, e
			}
			if offset < 0 || size < 0 || offset > entry.Size || size > entry.Size-offset || size > 4<<20 {
				return nil, bad12(10003, "fragment outside bounds or larger than 4 MiB")
			}
			fd, e := os.Open(entry.Path)
			if e != nil {
				return nil, e
			}
			defer fd.Close()
			data := make([]byte, size)
			if _, e = fd.ReadAt(data, offset); e != nil && e != io.EOF {
				return nil, e
			}
			return map[string]any{"data": data}, nil
		default:
			return nil, bad12(10003, "stage must be prepare or transfer")
		}
	}
	typ, e := stringParam12(p, "type", true)
	if e != nil {
		return nil, e
	}
	result := map[string]any{"name": entry.Name, "sha256": entry.SHA}
	switch typ {
	case "path":
		result["path"] = entry.Path
	case "data":
		data, e := boundedFile12(entry.Path)
		if e != nil {
			return nil, e
		}
		result["data"] = data
	case "url":
		if entry.URL == "" {
			return nil, bad12(10004, "this file has no externally downloadable URL; use data or path")
		}
		result["url"] = entry.URL
	default:
		return nil, bad12(10004, "unsupported get_file type")
	}
	return result, nil
}
