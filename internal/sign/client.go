// Package sign talks to a packet signing service.
//
// The contract is the one the ecosystem settled on: POST a command, a sequence number
// and the raw packet, get back hex sign/extra/token. It is deliberately transport-only
// — nothing here generates a signature, so pointing it at a real signer is the whole
// configuration.
package sign

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Bundle is one signature set as the client protocol expects it.
type Bundle struct {
	Sign        []byte
	Extra       []byte
	DeviceToken []byte
}

type request struct {
	Cmd  string `json:"cmd"`
	Seq  uint32 `json:"seq"`
	Data string `json:"data"`
}

type response struct {
	Sign        string `json:"sign"`
	Extra       string `json:"extra"`
	DeviceToken string `json:"deviceToken"`
}

// Client signs packets against one endpoint.
type Client struct {
	endpoint      string
	http          *http.Client
	mu            sync.RWMutex
	session       string
	managed       bool
	control       sync.Mutex
	resumeKey     string
	epoch         string
	resumable     bool
	guid          string
	uin           uint32
	logged        bool
	statePath     string
	pending       *pendingReply
	lastPullID    string
	lastPullEpoch string
}

// New builds a client for an endpoint. The timeout bounds one signing call; a signer
// that is slow or unreachable must fail the login rather than hang it.
func New(endpoint string, timeout time.Duration) *Client {
	return &Client{endpoint: endpoint, http: &http.Client{Timeout: timeout}}
}

// Endpoint reports the service this client signs against.
func (c *Client) Endpoint() string { return c.endpoint }

// The queue wait is part of the caller's timeout, including O3 cancellation.
func (c *Client) lockControl(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.control.TryLock() {
		return nil
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := ctx.Err(); err != nil {
				return err
			}
			if c.control.TryLock() {
				return nil
			}
		}
	}
}

// Sign asks the service for a signature over one packet.
func (c *Client) Sign(ctx context.Context, cmd string, seq uint32, packet []byte) (Bundle, error) {
	budget := c.http.Timeout
	if budget <= 0 {
		budget = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	body, err := json.Marshal(request{Cmd: cmd, Seq: seq, Data: base64.StdEncoding.EncodeToString(packet)})
	if err != nil {
		return Bundle{}, fmt.Errorf("encode sign request: %w", err)
	}
	if err := c.lockControl(ctx); err != nil {
		return Bundle{}, err
	}
	resumable := c.resumable
	c.control.Unlock()
	if resumable {
		id, err := requestID()
		if err != nil {
			return Bundle{}, err
		}
		for {
			if err := c.lockControl(ctx); err != nil {
				return Bundle{}, err
			}
			resp, err := c.managedRequest(ctx, http.MethodPost, "/sign", body, id, "")
			c.control.Unlock()
			if errors.Is(err, errEpochChanged) {
				id, err = requestID()
				if err != nil {
					return Bundle{}, err
				}
				continue
			}
			if errors.Is(err, errRecovering) {
				timer := time.NewTimer(250 * time.Millisecond)
				select {
				case <-ctx.Done():
					timer.Stop()
					return Bundle{}, ctx.Err()
				case <-timer.C:
				}
				continue
			}
			if err != nil {
				return Bundle{}, err
			}
			defer resp.Body.Close()
			if resp.StatusCode != 200 {
				return Bundle{}, fmt.Errorf("signer returned HTTP %d", resp.StatusCode)
			}
			var decoded response
			if err = json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
				return Bundle{}, err
			}
			return decodeBundle(decoded)
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return Bundle{}, fmt.Errorf("build sign request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	c.sessionHeader(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return Bundle{}, fmt.Errorf("call signer: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Bundle{}, fmt.Errorf("signer returned HTTP %d", resp.StatusCode)
	}
	var decoded response
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return Bundle{}, fmt.Errorf("decode sign response: %w", err)
	}
	return decodeBundle(decoded)
}

func decodeBundle(value response) (Bundle, error) {
	signature, err := decodeHexField("sign", value.Sign)
	if err != nil {
		return Bundle{}, err
	}
	if len(signature) == 0 {
		return Bundle{}, fmt.Errorf("signer returned an empty signature")
	}
	extra, err := decodeHexField("extra", value.Extra)
	if err != nil {
		return Bundle{}, err
	}
	token, err := decodeHexField("deviceToken", value.DeviceToken)
	if err != nil {
		return Bundle{}, err
	}
	return Bundle{Sign: signature, Extra: extra, DeviceToken: token}, nil
}

// decodeHexField decodes an optional hex field; the protocol allows the optional ones
// to be absent or empty, but never malformed.
func decodeHexField(name, value string) ([]byte, error) {
	if value == "" {
		return nil, nil
	}
	decoded, err := hex.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("signer returned invalid hex in %s: %w", name, err)
	}
	return decoded, nil
}
