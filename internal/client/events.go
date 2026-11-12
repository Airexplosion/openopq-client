package client

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"openopq/internal/protocol"
	"openopq/internal/transport"
)

// The bot consumes the server's pushes and broadcasts each incoming chat message — decoded
// once, as a protocol.MessageEvent — to its subscribers. The HTTP layer's protocol adapters
// (OPQ `/ws`, OneBot `/onebot/v11/ws`) each format their own wire shape from it.

type subscriberSet struct {
	mu  sync.Mutex
	set map[chan protocol.MessageEvent]struct{}
}

func (s *subscriberSet) closeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.set {
		delete(s.set, ch)
		close(ch)
	}
}

// Subscribe registers a listener and returns a channel plus a cancel func. Each ws connection
// subscribes and cancels when it closes.
func (b *Bot) Subscribe() (<-chan protocol.MessageEvent, func()) {
	ch := make(chan protocol.MessageEvent, 128)
	b.subs.mu.Lock()
	if b.subs.set == nil {
		b.subs.set = make(map[chan protocol.MessageEvent]struct{})
	}
	b.subs.set[ch] = struct{}{}
	b.subs.mu.Unlock()
	cancel := func() {
		b.subs.mu.Lock()
		if _, ok := b.subs.set[ch]; ok {
			delete(b.subs.set, ch)
			close(ch)
		}
		b.subs.mu.Unlock()
	}
	return ch, cancel
}

func (b *Bot) broadcast(event protocol.MessageEvent) {
	b.subs.mu.Lock()
	defer b.subs.mu.Unlock()
	for ch := range b.subs.set {
		select {
		case ch <- event:
		default: // never block the reader on a slow subscriber
		}
	}
}

// readEvents consumes one connection's pushes. The supervisor decides whether a
// closed transport can be restored or a server refusal needs a fresh login.
func (b *Bot) readEvents(ctx context.Context, conn Connection, exchange Exchange, self uint32) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := exchangeAuthenticationError(exchange); err != nil {
			return err
		}
		b.mu.Lock()
		beat := b.beat
		b.mu.Unlock()
		if err := beat.HealthError(); err != nil {
			return err
		}
		receiveCtx, cancel := context.WithTimeout(ctx, time.Second)
		resp, err := conn.Receive(receiveCtx)
		cancel()
		if err != nil {
			closed, hasClosed := conn.(interface{ Closed() bool })
			if errors.Is(err, transport.ErrClosed) || (hasClosed && closed.Closed()) || (!hasClosed && isNetworkFailure(err)) {
				return err
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				if !waitRecovery(ctx, 50*time.Millisecond) {
					return ctx.Err()
				}
			}
			continue
		}
		if resp.Code == -10003 {
			return &Refusal{Command: resp.Command, Code: resp.Code, Message: resp.Message}
		}
		if resp.Command == "trpc.qq_new_tech.status_svc.StatusService.KickNT" || resp.Command == "MessageSvc.PushForceOffline" || resp.Command == "StatSvc.ReqMSFOffline" {
			return &sessionTerminated{reason: "QQ 服务端要求下线，需要重新登录"}
		}
		if resp.Command != protocol.PushMessageCommand {
			continue
		}
		m, notice, err := protocol.DecodePush(resp.Body)
		if err != nil {
			b.reconnectLog(slog.LevelWarn, "QQ push decode failed", "account", self, "sequence", resp.Sequence, "error", err)
			continue
		}
		if notice != nil {
			// A membership change, recall, mute, admin change or poke. Fill the uins it reports
			// from what the bot has learned, then broadcast it.
			b.resolveNotice(notice)
			if notice.Kind == protocol.NoticeFriendRequest || notice.Kind == protocol.NoticeGroupRequest {
				ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
				err := b.prepareRequest(ctx, notice)
				cancel()
				if err != nil {
					fmt.Fprintf(os.Stderr, "[onebot] request event not published: self=%d kind=%s error=%v\n", self, notice.Kind, err)
					continue
				}
			}
			b.broadcast(protocol.MessageEvent{SelfUin: self, Notice: notice})
			continue
		}
		if m == nil {
			continue
		}
		// Learn who is who: the sender's uid and nickname, keyed by uin, so a later @mention of
		// them can be addressed the way the NT server expects and shown with their name.
		b.enrichSender(ctx, m)
		b.observeTemporaryMessage(m)
		b.obMedia.observe(m)
		b.broadcast(protocol.MessageEvent{SelfUin: self, Msg: m})
	}
}
