package console

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"openopq/internal/bot"
	"openopq/internal/client"
	"openopq/internal/protocol"
)

func (b *attachedOPQBot) Self(ctx context.Context) (bot.Self, error) {
	req, e := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(b.endpoint.String(), "/")+"/console/api/status", nil)
	if e != nil {
		return bot.Self{}, e
	}
	res, e := http.DefaultClient.Do(req)
	if e != nil {
		return bot.Self{}, e
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return bot.Self{}, fmt.Errorf("attached status HTTP %d", res.StatusCode)
	}
	var state State
	if e = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&state); e != nil {
		return bot.Self{}, e
	}
	var uin uint32
	_, _ = fmt.Sscan(state.Account, &uin)
	return bot.Self{Uin: uin, Nickname: state.Nickname, Online: state.Online}, nil
}
func (b *attachedOPQBot) OneBotAction(ctx context.Context, action string, params json.RawMessage) (any, error) {
	if b.queries != nil {
		switch action {
		case "get_friend_list", "get_group_member_info", "get_group_member_list", "get_stranger_info", "send_like":
			return b.queries.OneBotAction(ctx, action, params)
		}
	}
	var reply struct {
		Status  string
		Retcode int
		Message string
		Data    json.RawMessage
	}
	if e := b.request(ctx, "/onebot/v11/"+action, params, &reply); e != nil {
		return nil, e
	}
	if reply.Status != "ok" {
		if strings.Contains(reply.Message, "unsupported action") {
			return nil, fmt.Errorf("%w: %s", bot.ErrUnsupported, action)
		}
		return nil, fmt.Errorf("attached action failed (%d): %s", reply.Retcode, reply.Message)
	}
	return reply.Data, nil
}

func newAttachedBot(u *url.URL) *attachedOPQBot {
	b := &attachedOPQBot{endpoint: u}
	b.queries = client.NewOneBotQueryBackend(attachedExchange{b})
	return b
}
func (b *attachedOPQBot) SupportedOneBotActions() []string {
	return []string{"get_friend_list", "get_group_member_list", "get_group_member_info", "get_stranger_info", "send_like", "set_group_kick", "set_group_ban", "set_group_whole_ban", "set_group_admin", "set_group_card", "set_group_name", "set_group_leave", "set_group_special_title"}
}

type attachedExchange struct{ bot *attachedOPQBot }

func (e attachedExchange) Exchange(ctx context.Context, command string, request []byte) ([]byte, error) {
	envelope, err := protocol.UnwrapOIDB(request)
	if err != nil {
		return nil, err
	}
	if envelope.Command.String() != command {
		return nil, fmt.Errorf("query command mismatch")
	}
	params := mustJSON(map[string]any{"body": hex.EncodeToString(envelope.Body), "reserved": envelope.Reserved})
	result, err := e.bot.Call(ctx, command, params)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	var data struct {
		Body     string
		Reserved uint64
	}
	if err = json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	body, err := hex.DecodeString(data.Body)
	if err != nil {
		return nil, err
	}
	return protocol.BuildOIDB(protocol.OIDBRequest{Command: envelope.Command, Body: body, Reserved: data.Reserved}), nil
}
func (b *attachedOPQBot) RecallGroupMessage(ctx context.Context, group, seq uint32) error {
	_, e := b.OneBotAction(ctx, "delete_msg", mustJSON(map[string]any{"group_id": group, "message_id": int32(seq)}))
	return e
}
func mustJSON(value any) json.RawMessage { b, _ := json.Marshal(value); return b }
func (b *attachedOPQBot) SubscribeOneBot() (<-chan []byte, func()) {
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan []byte, 128)
	go func() {
		defer close(out)
		for ctx.Err() == nil {
			u := *b.endpoint
			u.Scheme = "ws"
			u.Path = strings.TrimRight(u.Path, "/") + "/onebot/v11/ws"
			d := websocket.Dialer{HandshakeTimeout: 3 * time.Second}
			c, res, e := d.DialContext(ctx, u.String(), nil)
			if res != nil && res.Body != nil {
				res.Body.Close()
			}
			if e == nil {
				done := make(chan struct{})
				go func() {
					select {
					case <-ctx.Done():
						c.Close()
					case <-done:
					}
				}()
				c.SetReadLimit(8 << 20)
				for {
					_, data, e := c.ReadMessage()
					if e != nil {
						break
					}
					select {
					case out <- data:
					case <-ctx.Done():
						c.Close()
						close(done)
						return
					}
				}
				close(done)
				c.Close()
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(3 * time.Second):
			}
		}
	}()
	return out, cancel
}
