package client

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"openopq/internal/protocol"
)

// Cache only personal nicknames. Group cards live in m.GroupCard and must never
// become another context's nickname (e.g. a private message after a group rename).
func (b *Bot) enrichSender(ctx context.Context, m *protocol.IncomingMessage) {
	b.remember(m.SenderUin, m.SenderUid, m.SenderNick)
	if m.SenderNick == "" {
		m.SenderNick = b.nickFor(m.SenderUin)
	}
	if m.SenderNick != "" || m.IsGroup || m.SenderUin == 0 {
		return
	}
	now := time.Now()
	b.mu.Lock()
	if until := b.profileFailures[m.SenderUin]; now.Before(until) {
		b.mu.Unlock()
		return
	}
	if b.profileFailures == nil {
		b.profileFailures = map[uint32]time.Time{}
	}
	// Bound negative entries and avoid one user generating a query per message.
	if len(b.profileFailures) >= 256 {
		var oldest uint32
		var when time.Time
		for id, until := range b.profileFailures {
			if when.IsZero() || until.Before(when) {
				oldest, when = id, until
			}
		}
		delete(b.profileFailures, oldest)
	}
	b.profileFailures[m.SenderUin] = now.Add(time.Minute)
	b.mu.Unlock()
	queryCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	params, _ := json.Marshal(map[string]uint32{"user_id": m.SenderUin})
	// Errors/empty profiles do not discard or invent the original message/name.
	_, err := b.OneBotAction(queryCtx, "get_stranger_info", params)
	if err != nil {
		b.reconnectLog(slog.LevelDebug, "sender nickname lookup unavailable", "sender", m.SenderUin, "error", err)
	}
	m.SenderNick = b.nickFor(m.SenderUin)
}
