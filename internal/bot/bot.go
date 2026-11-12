// Package bot is the protocol layer an implementation has to provide. Keeping it an
// interface is what lets the HTTP surface be written and tested before the client
// protocol underneath is finished.
package bot

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// ErrNotConnected is returned when the framework has no live session yet.
var ErrNotConnected = errors.New("not connected")
var ErrUnsupported = errors.New("unsupported QQ backend operation")

type SavedLoginStatus struct {
	Saved      bool   `json:"saved"`
	Restorable bool   `json:"restorable"`
	AutoResume bool   `json:"auto_resume"`
	Reason     string `json:"reason,omitempty"`
}

// Self describes the signed-in account.
type Self struct {
	Uin      uint32 `json:"Uin"`
	UID      string `json:"Uid,omitempty"`
	Nickname string `json:"NickName"`
	Online   bool   `json:"Online"`
}

// ReconnectStatus exposes recovery progress without exposing login tickets.
type ReconnectStatus struct {
	Reconnecting  bool       `json:"reconnecting"`
	Attempt       int        `json:"attempt"`
	NextRetryAt   *time.Time `json:"next_retry_at,omitempty"`
	LastError     string     `json:"last_error,omitempty"`
	RequiresLogin bool       `json:"requires_login"`
}

// HeartbeatStatus separates confirmed QQ replies from signing and local writes.
type HeartbeatStatus struct {
	ConnectionSince     time.Time  `json:"connection_since"`
	LastAttemptAt       *time.Time `json:"last_attempt_at,omitempty"`
	LastSignedAt        *time.Time `json:"last_signed_at,omitempty"`
	LastSentAt          *time.Time `json:"last_sent_at,omitempty"`
	LastReplyAt         *time.Time `json:"last_reply_at,omitempty"`
	LastSuccessAt       *time.Time `json:"last_success_at,omitempty"`
	LastReplyCode       *int32     `json:"last_reply_code,omitempty"`
	Stage               string     `json:"stage"`
	LastFailureStage    string     `json:"last_failure_stage,omitempty"`
	LastError           string     `json:"last_error,omitempty"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
	Stale               bool       `json:"stale"`
}

// Group is one group the account belongs to.
type Group struct {
	GroupUin       uint32 `json:"GroupUin"`
	GroupName      string `json:"GroupName"`
	MemberCount    int    `json:"MemberCount"`
	MaxMemberCount int    `json:"MaxMemberCount"`
	// Metadata used by OneBot. Keep the original OPQ group envelope stable.
	CreateTime         uint32 `json:"-"`
	Remark             string `json:"-"`
	Memo               string `json:"-"`
	Description        string `json:"-"`
	ShutUpAllTimestamp uint32 `json:"-"`
}

// Bot is the surface the API layer drives.
type Bot interface {
	// QRCode returns a PNG of the login QR code.
	QRCode(ctx context.Context) ([]byte, error)
	// LoadSession restores a previously saved session.
	LoadSession(ctx context.Context) error
	// Self reports the current account.
	Self(ctx context.Context) (Self, error)
	// Groups lists the account's groups.
	Groups(ctx context.Context) ([]Group, error)
	// Call dispatches a named protocol operation, which is how most of the API works.
	Call(ctx context.Context, function string, params json.RawMessage) (any, error)
}
