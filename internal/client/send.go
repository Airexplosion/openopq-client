package client

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"sync/atomic"
	"time"

	"openopq/internal/protocol"
)

// SendMessage sends a plain-text message to a group or a friend and returns the sequence the
// service assigned it. It is the outgoing half of the OPQ-compatible message interface; the
// HTTP layer maps an OPQ send request onto it.
func (b *Bot) SendMessage(ctx context.Context, target protocol.SendTarget, text string, ats []protocol.SendAt) (protocol.SendResult, error) {
	target, err := b.PrepareSendTarget(ctx, target)
	if err != nil {
		return protocol.SendResult{}, err
	}
	exchange, err := b.service()
	if err != nil {
		return protocol.SendResult{}, err
	}
	// NT private routing needs the peer's UID too. Only use this bot's learned mapping;
	// a caller-supplied UID takes precedence, and group routing has no private peer.
	if !target.IsGroup() && target.Uid == "" {
		target.Uid = b.uidFor(target.Uin)
	}
	// A mention addressed only by uin needs the uid the NT server binds on; fill it from what
	// received messages have taught us about who is who.
	for i := range ats {
		if ats[i].Uid == "" {
			if uid := b.uidFor(ats[i].Uin); uid != "" {
				ats[i].Uid = uid
			}
		}
		if ats[i].Nick == "" {
			if nick := b.nickFor(ats[i].Uin); nick != "" {
				ats[i].Nick = nick
			}
		}
	}
	clientSeq := b.nextMessageSeq()
	random := randU32()
	msgFlag := uint32(time.Now().Unix())
	body := protocol.BuildSendMessage(target, text, ats, clientSeq, random, msgFlag)
	answer, err := exchange.Exchange(ctx, protocol.SendMessageCommand, body)
	if err != nil {
		return protocol.SendResult{}, err
	}
	result, err := protocol.ParseSendResponse(answer, target.IsTemporary())
	result.ClientSequence, result.Random = clientSeq, random
	return result, err
}

// nextMessageSeq returns the next client message sequence, seeding it once from a random base
// so a fresh client does not start at one.
func (b *Bot) nextMessageSeq() uint32 {
	for {
		cur := atomic.LoadUint32(&b.msgSeq)
		next := cur + 1
		if cur == 0 {
			next = 0x3000000 + (randU32() % 0x1000000) // seed
		}
		if atomic.CompareAndSwapUint32(&b.msgSeq, cur, next) {
			return next
		}
	}
}

func randU32() uint32 {
	var x [4]byte
	if _, err := rand.Read(x[:]); err != nil {
		return 1
	}
	return binary.BigEndian.Uint32(x[:])
}

func (b *Bot) sendElements(ctx context.Context, target protocol.SendTarget, elems [][]byte) (protocol.SendResult, error) {
	target, err := b.PrepareSendTarget(ctx, target)
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
	ex, e := b.service()
	if e != nil {
		return protocol.SendResult{}, e
	}
	seq, random := b.nextMessageSeq(), randU32()
	data, e := ex.Exchange(ctx, protocol.SendMessageCommand, protocol.BuildMediaMessage(target, elems, seq, random, uint32(time.Now().Unix())))
	if e != nil {
		return protocol.SendResult{}, e
	}
	result, e := protocol.ParseSendResponse(data, target.IsTemporary())
	result.ClientSequence, result.Random = seq, random
	return result, e
}

// SendRichMessage uploads each image first, then sends exactly one message with
// text, mentions, faces and images in the caller's original order.
func (b *Bot) SendRichMessage(ctx context.Context, target protocol.SendTarget, segments []protocol.SendSegment) (protocol.SendResult, error) {
	if len(segments) == 0 || len(segments) > 100 {
		return protocol.SendResult{}, fmt.Errorf("message needs 1..100 segments")
	}
	var total int
	images := 0
	for _, s := range segments {
		switch s.Type {
		case "text", "at", "face":
		case "image":
			images++
			total += len(s.Bytes)
			if len(s.Bytes) == 0 || len(s.Bytes) > 30<<20 {
				return protocol.SendResult{}, fmt.Errorf("invalid image size")
			}
		default:
			return protocol.SendResult{}, fmt.Errorf("unsupported rich segment %s", s.Type)
		}
	}
	if images > 20 || total > 60<<20 {
		return protocol.SendResult{}, fmt.Errorf("message exceeds image limits")
	}
	var err error
	target, err = b.PrepareSendTarget(ctx, target)
	if err != nil {
		return protocol.SendResult{}, err
	}
	var elems [][]byte
	for _, s := range segments {
		switch s.Type {
		case "text":
			elems = append(elems, protocol.TextElem(s.Text))
		case "at":
			at := s.At
			if at.Uin != 0 && at.Uid == "" {
				at.Uid = b.uidFor(at.Uin)
				if at.Uid == "" {
					if target.IsGroup() {
						_, e := b.oneBotMembers(ctx, target.Group)
						if e != nil {
							return protocol.SendResult{}, e
						}
					} else {
						_, e := b.oneBotFriends(ctx)
						if e != nil {
							return protocol.SendResult{}, e
						}
					}
					at.Uid = b.uidFor(at.Uin)
				}
			}
			if at.Uin != 0 && at.Uid == "" {
				return protocol.SendResult{}, fmt.Errorf("mention UID is unknown")
			}
			if at.Nick == "" {
				at.Nick = b.nickFor(at.Uin)
			}
			elems = append(elems, protocol.AtElem(at))
		case "face":
			elems = append(elems, protocol.Marshal(protocol.BytesField(2, protocol.Marshal(protocol.VarintField(1, uint64(s.Face))))))
		case "image":
			image, e := b.prepareImage(ctx, target, s.Bytes)
			if e != nil {
				return protocol.SendResult{}, e
			}
			elems = append(elems, image...)
		}
	}
	return b.sendElements(ctx, target, elems)
}
