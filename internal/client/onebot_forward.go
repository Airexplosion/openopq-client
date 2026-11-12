package client

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"openopq/internal/protocol"
)

func (b *Bot) forwardAction(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		ID string `json:"id"`
	}
	if e := json.Unmarshal(raw, &p); e != nil {
		return nil, e
	}
	if p.ID == "" || len(p.ID) > 2048 || strings.ContainsAny(p.ID, "\x00\r\n") {
		return nil, fmt.Errorf("valid forward id required")
	}
	ex, e := b.service()
	if e != nil {
		return nil, e
	}
	uid := b.Credentials().Uid
	data, e := ex.Exchange(ctx, protocol.ReceiveForwardCommand, protocol.BuildReceiveForward(uid, p.ID))
	if e != nil {
		return nil, e
	}
	messages, e := protocol.ParseReceiveForward(data, p.ID)
	if e != nil {
		return nil, e
	}
	for _, m := range messages {
		b.obMedia.observe(m)
	}
	// The adapter assigns account-scoped message IDs and the selected v11
	// message format. Native transport metadata never goes onto the wire.
	return messages, nil
}
