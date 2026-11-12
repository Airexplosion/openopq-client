package sign

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

var sessionTakeoverAt = []time.Duration{0, 200 * time.Millisecond, time.Second, 125 * time.Second}

func (c *Client) base() string {
	u, err := url.Parse(c.endpoint)
	if err != nil {
		return ""
	}
	u.Path = strings.TrimSuffix(strings.TrimRight(u.Path, "/"), "/sign")
	u.RawQuery, u.Fragment = "", ""
	return strings.TrimRight(u.String(), "/")
}
func (c *Client) sessionHeader(req *http.Request) {
	c.mu.RLock()
	token := c.session
	c.mu.RUnlock()
	if token != "" {
		req.Header.Set("X-OPQ-Session", token)
	}
}

// Prepare only enables session handling when the endpoint explicitly advertises it.
// Existing signature services keep their original POST /sign contract.
func (c *Client) Prepare(ctx context.Context, guid []byte, uin uint32) error {
	if err := c.lockControl(ctx); err != nil {
		return err
	}
	defer c.control.Unlock()
	if len(guid) != 16 {
		return fmt.Errorf("signer requires a 16-byte device GUID")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base()+"/capabilities", nil)
	if err != nil {
		return err
	}
	response, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("check signer capabilities: %w", err)
	}
	var caps struct {
		Sessions bool `json:"sessions"`
		Resume   bool `json:"resume"`
	}
	if response.StatusCode == 200 {
		_ = json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&caps)
	}
	response.Body.Close()
	if !caps.Sessions {
		return nil
	}
	c.guid = hex.EncodeToString(guid)
	c.uin = uin
	c.resumable = caps.Resume
	if c.resumable {
		if err := c.loadState(); err != nil {
			if !errors.Is(err, errCheckpointIdentity) {
				return err
			}
			c.resumeKey, c.pending = "", nil
		}
		if c.resumeKey != "" {
			if err := c.resume(ctx); err == nil {
				return nil
			} else if !errors.Is(err, errResumeDenied) {
				// Only a rejected secret permits a new login session. A network
				// failure must not silently take over an existing SDK identity.
				return err
			}
			c.resumeKey = ""
			c.pending = nil
		}
	}
	started := time.Now()
	var last error
	for i, at := range sessionTakeoverAt {
		if i > 0 {
			if wait := at - time.Since(started); wait > 0 {
				if err := waitCtx(ctx, wait); err != nil {
					return err
				}
			}
			if c.resumeKey != "" {
				if err := c.resume(ctx); err == nil {
					return nil
				}
			}
		}
		last = c.createSession(ctx, guid, uin)
		if last == nil || !errors.Is(last, errIdentityActive) {
			return last
		}
	}
	return last
}

func waitCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func sessionErrorCode(text string) string {
	if len(text) == 0 || len(text) > 100 {
		return ""
	}
	for _, character := range text {
		if !(character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '_') {
			return ""
		}
	}
	return text
}

func (c *Client) createSession(ctx context.Context, guid []byte, uin uint32) error {
	body, _ := json.Marshal(map[string]any{"guid": hex.EncodeToString(guid), "uin": uin})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base()+"/session", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("prepare signer device: %w", err)
	}
	defer response.Body.Close()
	rawBody, err := io.ReadAll(io.LimitReader(response.Body, 4096))
	if err != nil {
		return fmt.Errorf("prepare signer device: %w", err)
	}
	if response.StatusCode != 200 {
		var detail struct {
			Error string `json:"error"`
		}
		code := ""
		if json.Unmarshal(rawBody, &detail) == nil {
			code = sessionErrorCode(detail.Error)
		}
		err := fmt.Errorf("prepare signer device returned HTTP %d", response.StatusCode)
		if code != "" {
			err = fmt.Errorf("prepare signer device returned HTTP %d (%s)", response.StatusCode, code)
		}
		if code == "identity_already_active" {
			return errors.Join(err, errIdentityActive)
		}
		return err
	}
	var prepared struct {
		Session   string `json:"session"`
		ResumeKey string `json:"resumeKey"`
		Epoch     string `json:"epoch"`
	}
	if err = json.Unmarshal(rawBody, &prepared); err != nil {
		return fmt.Errorf("decode signer session: %w", err)
	}
	raw, err := hex.DecodeString(prepared.Session)
	if err != nil || len(raw) != 32 {
		return fmt.Errorf("invalid signer session")
	}
	c.mu.Lock()
	c.session = prepared.Session
	c.managed = true
	c.mu.Unlock()
	if c.resumable {
		key, e := hex.DecodeString(prepared.ResumeKey)
		if e != nil || len(key) != 32 || len(prepared.Epoch) != 32 {
			return fmt.Errorf("invalid signer resume metadata")
		}
		c.resumeKey, c.epoch = prepared.ResumeKey, prepared.Epoch
		return c.saveState()
	}
	return nil
}

// O3Session binds the control requests to the same native process as Sign.
func (c *Client) O3Session() (string, string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.managed {
		return "", ""
	}
	return c.base(), c.session
}
func (c *Client) Account(ctx context.Context, uin uint32, loggedIn bool) error {
	if err := c.lockControl(ctx); err != nil {
		return err
	}
	defer c.control.Unlock()
	_, token := c.O3Session()
	if token == "" {
		return nil
	}
	body, _ := json.Marshal(map[string]any{"uin": uin, "loggedIn": loggedIn})
	if c.resumable {
		response, err := c.managedRequest(ctx, http.MethodPost, "/account", body, "", "")
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if response.StatusCode != 200 {
			return fmt.Errorf("notify signer account returned HTTP %d", response.StatusCode)
		}
		if uin != 0 {
			c.uin = uin
		}
		c.logged = loggedIn
		return c.saveState()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base()+"/account", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	c.sessionHeader(req)
	response, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("notify signer account: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return fmt.Errorf("notify signer account returned HTTP %d", response.StatusCode)
	}
	return nil
}
func (c *Client) Release() error {
	c.control.Lock()
	defer c.control.Unlock()
	c.mu.Lock()
	token := c.session
	c.session = ""
	c.managed = false
	c.mu.Unlock()
	if token == "" {
		return nil
	}
	c.resumeKey = ""
	c.pending = nil
	c.logged = false
	if c.statePath != "" {
		if err := os.Remove(c.statePath); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.base()+"/session", nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-OPQ-Session", token)
	response, err := c.http.Do(req)
	if err != nil {
		return err
	}
	response.Body.Close()
	return nil
}
