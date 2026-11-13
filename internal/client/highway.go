package client

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"time"

	"openopq/internal/protocol"
)

// Image send over Highway: the rich-media upload request yields a Highway ukey + servers + the
// MsgInfo; the file bytes go up the Highway TCP channel; the message is then sent carrying the
// MsgInfo as a CommonElem. The Highway session (servers + ticket) is fetched once and cached.

const highwayBlockSize = 1024 * 1024

// ensureHighway fetches and caches the Highway session (servers + ticket) if not already held.
func (b *Bot) ensureHighway(ctx context.Context) (protocol.HighwaySession, error) {
	b.mu.Lock()
	if len(b.highway.Servers) > 0 && len(b.highway.SigSession) > 0 {
		s := b.highway
		b.mu.Unlock()
		return s, nil
	}
	tgt := b.account.Token
	b.mu.Unlock()

	exchange, err := b.service()
	if err != nil {
		return protocol.HighwaySession{}, err
	}
	answer, err := exchange.Exchange(ctx, protocol.HighwayURLCommand, protocol.BuildHighwayURLReq(tgt))
	if err != nil {
		return protocol.HighwaySession{}, fmt.Errorf("highway server request: %w", err)
	}
	session, err := protocol.ParseHighwayURLResp(answer)
	if err != nil {
		return protocol.HighwaySession{}, err
	}
	if len(session.Servers) == 0 || len(session.SigSession) == 0 {
		return protocol.HighwaySession{}, fmt.Errorf("highway session incomplete")
	}
	b.mu.Lock()
	b.highway = session
	b.mu.Unlock()
	return session, nil
}

// highwayUpload sends the file bytes up the Highway TCP channel, one block at a time.
func (b *Bot) highwayUpload(ctx context.Context, session protocol.HighwaySession, commandID uint32,
	data, fileMd5, ext []byte) error {
	b.mu.Lock()
	uin, dataAppID, loginSigAppID, ticket, loginSig := b.account.Uin, b.account.AppID,
		b.options.Device.SubAppID, session.SigSession, b.account.Token
	b.mu.Unlock()

	addr := session.Servers[0].Addr()
	dialer := net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("highway dial %s: %w", addr, err)
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(60 * time.Second))
	}

	fileSize := uint64(len(data))
	var seq uint32
	for offset := 0; offset < len(data); offset += highwayBlockSize {
		end := offset + highwayBlockSize
		if end > len(data) {
			end = len(data)
		}
		chunk := data[offset:end]
		sum := md5.Sum(chunk)
		s := atomic.AddUint32(&seq, 1)
		head := protocol.BuildHighwayHead(uin, loginSigAppID, dataAppID, s, commandID,
			fileSize, uint64(offset), uint32(len(chunk)), sum[:], fileMd5, ticket, loginSig, ext)
		if _, err := conn.Write(protocol.FrameHighway(head, chunk)); err != nil {
			return fmt.Errorf("highway write: %w", err)
		}
		code, err := readHighwayResp(conn)
		if err != nil {
			return err
		}
		if code != 0 {
			return fmt.Errorf("highway upload failed: code=%d", code)
		}
	}
	return nil
}

// readHighwayResp reads one Highway reply frame and returns the head's error code.
func readHighwayResp(conn net.Conn) (uint32, error) {
	var prefix [9]byte
	if _, err := io.ReadFull(conn, prefix[:]); err != nil {
		return 0, fmt.Errorf("highway read prefix: %w", err)
	}
	if prefix[0] != 0x28 {
		return 0, fmt.Errorf("highway reply not framed (0x%02x)", prefix[0])
	}
	headLen := binary.BigEndian.Uint32(prefix[1:5])
	bodyLen := binary.BigEndian.Uint32(prefix[5:9])
	if headLen > 1<<20 || bodyLen > 1<<24 {
		return 0, fmt.Errorf("highway reply oversized: head=%d body=%d", headLen, bodyLen)
	}
	buf := make([]byte, int(headLen)+int(bodyLen)+1) // + trailing 0x29
	if _, err := io.ReadFull(conn, buf); err != nil {
		return 0, fmt.Errorf("highway read body: %w", err)
	}
	return protocol.ParseHighwayRespHead(buf[:headLen])
}

// UploadAndSendImage uploads an image and sends it as a message to a group or a friend, with an
// optional text caption. For a friend the uid is resolved from what the bot has learned, as a
// mention is.
func (b *Bot) prepareImage(ctx context.Context, target protocol.SendTarget, data []byte) ([][]byte, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("client: empty image")
	}
	var err error
	target, err = b.PrepareSendTarget(ctx, target)
	if err != nil {
		return nil, err
	}
	isGroup := target.IsGroup()
	targetUID := target.Uid
	if !isGroup && targetUID == "" && b.uidFor(target.Uin) == "" {
		if _, e := b.oneBotFriends(ctx); e != nil {
			return nil, e
		}
	}
	if !isGroup && targetUID == "" {
		if uid := b.uidFor(target.Uin); uid != "" {
			targetUID = uid
		} else {
			return nil, fmt.Errorf("client: no uid known for uin %d", target.Uin)
		}
	}

	md5sum := md5.Sum(data)
	sha1sum := sha1.Sum(data)
	format, ext, w, h := protocol.ResolveImage(data)
	meta := protocol.MediaMeta{
		Size: uint32(len(data)), Md5: md5sum[:], Sha1: sha1sum[:],
		Width: w, Height: h, Format: format, Ext: ext,
	}

	exchange, err := b.service()
	if err != nil {
		return nil, err
	}
	reqBody, command := protocol.BuildImageUploadReq(target.Group, groupUID(isGroup, targetUID), meta, target.TempGroup)
	answer, err := exchange.Exchange(ctx, command, reqBody)
	if err != nil {
		return nil, fmt.Errorf("image upload request: %w", err)
	}
	up, err := protocol.ParseImageUploadResp(answer)
	if err != nil {
		return nil, err
	}

	// A ukey means the server wants the bytes; without one the file already exists (秒传).
	if up.UKey != "" {
		session, err := b.ensureHighway(ctx)
		if err != nil {
			return nil, err
		}
		if len(up.Servers) == 0 {
			up.Servers = session.Servers
		}
		ext := protocol.BuildHighwayExt(up, highwayBlockSize)
		commandID := uint32(1004) // group image
		if !isGroup {
			commandID = 1003 // private image
		}
		if err := b.highwayUpload(ctx, session, commandID, data, md5sum[:], ext); err != nil {
			return nil, err
		}
	}

	var elems [][]byte
	if compat := protocol.ImageCompatElem(up.CompatQMsg, isGroup); compat != nil {
		elems = append(elems, compat)
	}
	return append(elems, protocol.ImageCommonElem(up.MsgInfo, isGroup)), nil
}
func (b *Bot) UploadAndSendImage(ctx context.Context, target protocol.SendTarget, data []byte, caption string) (protocol.SendResult, error) {
	var err error
	target, err = b.PrepareSendTarget(ctx, target)
	if err != nil {
		return protocol.SendResult{}, err
	}
	elems, e := b.prepareImage(ctx, target, data)
	if e != nil {
		return protocol.SendResult{}, e
	}
	if caption != "" {
		elems = append([][]byte{protocol.TextElem(caption)}, elems...)
	}
	return b.sendElements(ctx, target, elems)
}

func (b *Bot) prepareVoice(ctx context.Context, target protocol.SendTarget, data []byte, duration uint32) ([][]byte, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("client: empty voice")
	}
	var err error
	target, err = b.PrepareSendTarget(ctx, target)
	if err != nil {
		return nil, err
	}
	isGroup := target.IsGroup()
	targetUID := target.Uid
	if !isGroup && targetUID == "" && b.uidFor(target.Uin) == "" {
		if _, e := b.oneBotFriends(ctx); e != nil {
			return nil, e
		}
	}
	if !isGroup && targetUID == "" {
		if uid := b.uidFor(target.Uin); uid != "" {
			targetUID = uid
		} else {
			return nil, fmt.Errorf("client: no uid known for uin %d", target.Uin)
		}
	}
	md5sum := md5.Sum(data)
	sha1sum := sha1.Sum(data)
	meta := protocol.MediaMeta{Size: uint32(len(data)), Md5: md5sum[:], Sha1: sha1sum[:], Duration: duration, IsVoice: true}

	exchange, err := b.service()
	if err != nil {
		return nil, err
	}
	reqBody, command := protocol.BuildVoiceUploadReq(target.Group, groupUID(isGroup, targetUID), meta, target.TempGroup)
	answer, err := exchange.Exchange(ctx, command, reqBody)
	if err != nil {
		return nil, fmt.Errorf("voice upload request: %w", err)
	}
	up, err := protocol.ParseImageUploadResp(answer)
	if err != nil {
		return nil, err
	}
	if up.UKey != "" {
		session, err := b.ensureHighway(ctx)
		if err != nil {
			return nil, err
		}
		if len(up.Servers) == 0 {
			up.Servers = session.Servers
		}
		ext := protocol.BuildHighwayExt(up, highwayBlockSize)
		commandID := uint32(1008) // group voice
		if !isGroup {
			commandID = 1007 // private voice
		}
		if err := b.highwayUpload(ctx, session, commandID, data, md5sum[:], ext); err != nil {
			return nil, err
		}
	}
	return [][]byte{protocol.VoiceCommonElem(up.MsgInfo)}, nil
}

// UploadAndSendVoice uploads a voice clip (SILK/AMR-encoded) and sends it. duration is seconds.
func (b *Bot) UploadAndSendVoice(ctx context.Context, target protocol.SendTarget, data []byte, duration uint32) (protocol.SendResult, error) {
	elems, err := b.prepareVoice(ctx, target, data, duration)
	if err != nil {
		return protocol.SendResult{}, err
	}
	return b.sendElements(ctx, target, elems)
}

// groupUID returns the targetUID for a c2c send, or empty for a group (which BuildImageUploadReq
// reads as "group").
func groupUID(isGroup bool, uid string) string {
	if isGroup {
		return ""
	}
	return uid
}
