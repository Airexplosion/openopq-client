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

// SendForwardMessage uploads a multi-message payload and sends the resid as a light-app.
func (b *Bot) SendForwardMessage(ctx context.Context, target protocol.SendTarget, nodes []protocol.ForwardNode) (protocol.SendResult, error) {
	if len(nodes) == 0 || len(nodes) > 200 {
		return protocol.SendResult{}, fmt.Errorf("forward needs 1..200 nodes")
	}
	var err error
	target, err = b.PrepareSendTarget(ctx, target)
	if err != nil {
		return protocol.SendResult{}, err
	}
	if !target.IsGroup() && target.Uid == "" {
		target.Uid = b.uidFor(target.Uin)
		if target.Uid == "" {
			if _, e := b.oneBotFriends(ctx); e != nil {
				return protocol.SendResult{}, e
			}
			target.Uid = b.uidFor(target.Uin)
		}
		if target.Uid == "" {
			return protocol.SendResult{}, fmt.Errorf("private peer UID is unknown")
		}
	}
	ex, err := b.service()
	if err != nil {
		return protocol.SendResult{}, err
	}
	selfUID := b.Credentials().Uid
	if selfUID == "" {
		self, e := b.Self(ctx)
		if e != nil {
			return protocol.SendResult{}, e
		}
		selfUID = self.UID
	}
	if selfUID == "" {
		return protocol.SendResult{}, fmt.Errorf("account UID is unknown")
	}
	answer, err := ex.Exchange(ctx, protocol.SendForwardCommand, protocol.BuildSendForward(selfUID, target.Group, nodes))
	if err != nil {
		return protocol.SendResult{}, err
	}
	resid, err := protocol.ParseSendForward(answer)
	if err != nil {
		return protocol.SendResult{}, err
	}
	return b.sendElements(ctx, target, [][]byte{protocol.ForwardLightAppElem(resid, nodes)})
}
