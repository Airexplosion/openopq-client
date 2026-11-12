package sign

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

type pendingReply struct {
	Epoch string
	Body  []byte
}
type savedSession struct {
	Endpoint, GUID, ResumeKey, Epoch string
	Uin                              uint32
	Logged                           bool
	Pending                          *pendingReply
}

var errEpochChanged = errors.New("signer restarted; old O3 response discarded")
var errRecovering = errors.New("signer is rebuilding O3 state")
var errResumeDenied = errors.New("signer resume credential was rejected")
var errCheckpointIdentity = errors.New("signer checkpoint identity mismatch")
var errIdentityActive = errors.New("identity_already_active")

func requestID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

// SetStatePath stores resume credentials and an unacknowledged O3 reply encrypted
// locally. It does not persist QQ login tickets or native SDK heap snapshots.
func (c *Client) SetStatePath(path string) {
	c.control.Lock()
	defer c.control.Unlock()
	c.statePath = path
}
func (c *Client) stateKey() ([]byte, error) {
	if err := os.MkdirAll(filepath.Dir(c.statePath), 0700); err != nil {
		return nil, err
	}
	path := c.statePath + ".key"
	key, err := os.ReadFile(path)
	if err == nil {
		if len(key) != 32 {
			return nil, fmt.Errorf("invalid signer state key")
		}
		return key, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	key = make([]byte, 32)
	if _, err = rand.Read(key); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if os.IsExist(err) {
		return os.ReadFile(path)
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err = f.Write(key); err != nil {
		return nil, err
	}
	if err = f.Sync(); err != nil {
		return nil, err
	}
	return key, nil
}
func (c *Client) saveState() error {
	if c.statePath == "" || !c.resumable {
		return nil
	}
	key, err := c.stateKey()
	if err != nil {
		return err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	value, err := json.Marshal(savedSession{c.endpoint, c.guid, c.resumeKey, c.epoch, c.uin, c.logged, c.pending})
	if err != nil {
		return err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return err
	}
	data := gcm.Seal(nonce, nonce, value, []byte("openopq-signer-resume-v1"))
	f, err := os.CreateTemp(filepath.Dir(c.statePath), ".signer-state-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(name, c.statePath)
}
func (c *Client) loadState() error {
	if c.statePath == "" {
		return nil
	}
	data, err := os.ReadFile(c.statePath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(data) > 8*1024*1024 {
		return fmt.Errorf("signer checkpoint too large")
	}
	key, err := c.stateKey()
	if err != nil {
		return err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	if len(data) < gcm.NonceSize()+gcm.Overhead() {
		return fmt.Errorf("invalid signer checkpoint")
	}
	plain, err := gcm.Open(nil, data[:gcm.NonceSize()], data[gcm.NonceSize():], []byte("openopq-signer-resume-v1"))
	if err != nil {
		return fmt.Errorf("signer checkpoint authentication failed")
	}
	var state savedSession
	if json.Unmarshal(plain, &state) != nil || state.Endpoint != c.endpoint || state.GUID != c.guid {
		return errCheckpointIdentity
	}
	if _, err := hex.DecodeString(state.ResumeKey); err != nil || len(state.ResumeKey) != 64 {
		return fmt.Errorf("invalid resume credential")
	}
	c.resumeKey, c.epoch, c.pending = state.ResumeKey, state.Epoch, state.Pending
	return nil
}
func readResponse(response *http.Response) ([]byte, error) {
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 8*1024*1024+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 8*1024*1024 {
		return nil, fmt.Errorf("signer response too large")
	}
	return data, nil
}
func bufferedResponse(response *http.Response, data []byte) *http.Response {
	response.Body = io.NopCloser(bytes.NewReader(data))
	return response
}
func errorCode(data []byte) string {
	var v struct{ Error string }
	_ = json.Unmarshal(data, &v)
	return v.Error
}
func (c *Client) oneRequest(ctx context.Context, method, route string, body []byte, id, epoch string) (*http.Response, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base()+route, bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c.sessionHeader(req)
	if id != "" {
		req.Header.Set("X-OPQ-Request-ID", id)
	}
	if epoch != "" {
		req.Header.Set("X-OPQ-Epoch", epoch)
	}
	response, err := c.http.Do(req)
	if err != nil {
		return nil, nil, err
	}
	data, err := readResponse(response)
	return response, data, err
}
func (c *Client) resume(ctx context.Context) error {
	if c.resumeKey == "" {
		return fmt.Errorf("signer resume credential unavailable")
	}
	body, _ := json.Marshal(map[string]any{"guid": c.guid, "resumeKey": c.resumeKey})
	response, data, err := c.oneRequest(ctx, http.MethodPost, "/session/resume", body, "", "")
	if err != nil {
		return err
	}
	if response.StatusCode != 200 {
		if response.StatusCode == 403 {
			return fmt.Errorf("%w: HTTP 403", errResumeDenied)
		}
		return fmt.Errorf("resume signer returned HTTP %d", response.StatusCode)
	}
	var v struct{ Session, Epoch string }
	if json.Unmarshal(data, &v) != nil || len(v.Epoch) != 32 {
		return fmt.Errorf("invalid resume response")
	}
	token, err := hex.DecodeString(v.Session)
	if err != nil || len(token) != 32 {
		return fmt.Errorf("invalid resumed session")
	}
	c.mu.Lock()
	c.session = v.Session
	c.managed = true
	c.mu.Unlock()
	c.epoch = v.Epoch
	return c.saveState()
}

// Retry the same signing request ID or O3 acknowledgement. Reconnect authenticates
// with a resume secret, never by guessing a GUID or silently creating a new owner.
func (c *Client) managedRequest(ctx context.Context, method, route string, body []byte, id, expectedEpoch string) (*http.Response, error) {
	budget := c.http.Timeout
	if budget <= 0 {
		budget = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	for attempt := 0; attempt < 40; attempt++ {
		if expectedEpoch != "" && expectedEpoch != c.epoch {
			return nil, errEpochChanged
		}
		response, data, err := c.oneRequest(ctx, method, route, body, id, expectedEpoch)
		if err == nil {
			code := errorCode(data)
			if response.StatusCode == 409 && (code == "prepare_signer_session_first" || code == "session_closed") {
				if err = c.resume(ctx); err == nil {
					continue
				}
				if errors.Is(err, errResumeDenied) {
					return nil, err
				}
			} else if response.StatusCode == 409 && (code == "signer_epoch_changed" || code == "sign_result_from_previous_epoch") {
				return nil, errEpochChanged
			} else if response.StatusCode == 503 && code == "signer_recovering" {
				return nil, errRecovering
			} else if response.StatusCode != 429 && !(response.StatusCode == 503 && (code == "signer_faulted" || code == "checkpoint_write_failed")) {
				return bufferedResponse(response, data), nil
			}
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		delay := time.Duration(attempt+1) * 100 * time.Millisecond
		if delay > time.Second {
			delay = time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return nil, fmt.Errorf("signer retry budget exhausted")
}

// DoO3 preserves an accepted server reply until the SDK acknowledges it. A new
// signer epoch discards that old callback and reconstructs a fresh handshake.
func (c *Client) DoO3(ctx context.Context, method, route string, body []byte) (*http.Response, error) {
	if err := c.lockControl(ctx); err != nil {
		return nil, err
	}
	defer c.control.Unlock()
	if !c.resumable {
		response, data, err := c.oneRequest(ctx, method, route, body, "", "")
		if err != nil {
			return nil, err
		}
		return bufferedResponse(response, data), nil
	}
	if route == "/o3/pull" && c.pending != nil {
		if err := c.flushReply(ctx); err != nil && !errors.Is(err, errEpochChanged) {
			return nil, err
		}
	}
	if route == "/o3/recv" {
		var feed struct{ Subseq json.RawMessage }
		if json.Unmarshal(body, &feed) != nil || callbackID(feed.Subseq) != c.lastPullID {
			return nil, fmt.Errorf("O3 reply does not match the pulled callback")
		}
		c.pending = &pendingReply{c.lastPullEpoch, append([]byte(nil), body...)}
		if err := c.saveState(); err != nil {
			return nil, err
		}
		if err := c.flushReply(ctx); err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewBufferString(`{"ok":true}`))}, nil
	}
	response, err := c.managedRequest(ctx, method, route, body, "", "")
	if err != nil {
		return nil, err
	}
	if route == "/o3/pull" && response.StatusCode == 200 {
		data, err := readResponse(response)
		if err != nil {
			return nil, err
		}
		var value struct {
			Empty  bool
			Subseq json.RawMessage
		}
		if json.Unmarshal(data, &value) != nil {
			return nil, fmt.Errorf("invalid O3 pull")
		}
		if !value.Empty {
			c.lastPullID = callbackID(value.Subseq)
			c.lastPullEpoch = response.Header.Get("X-OPQ-Epoch")
		}
		return bufferedResponse(response, data), nil
	}
	return response, nil
}
func callbackID(value json.RawMessage) string {
	var text string
	if json.Unmarshal(value, &text) == nil {
		return text
	}
	return string(value)
}
func (c *Client) flushReply(ctx context.Context) error {
	if c.pending == nil {
		return nil
	}
	response, err := c.managedRequest(ctx, http.MethodPost, "/o3/recv", c.pending.Body, "", c.pending.Epoch)
	if errors.Is(err, errEpochChanged) {
		c.pending = nil
		if saveErr := c.saveState(); saveErr != nil {
			return saveErr
		}
		return err
	}
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return fmt.Errorf("O3 acknowledgement returned HTTP %d", response.StatusCode)
	}
	c.pending = nil
	return c.saveState()
}
