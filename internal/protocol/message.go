package protocol

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
)

// PushMessageCommand is the service name a server-pushed chat message arrives under.
// A frame whose SSO header carries this command is an incoming message, not a reply.
const PushMessageCommand = "trpc.msg.olpush.OlPushService.MsgPush"

// IncomingMessage is one pushed message decoded to the fields the OPQ event format needs.
// The protobuf shape is the server's own (PushMsg → PushMsgBody{ResponseHead, ContentHead,
// Body}); the field numbers are read off real pushes (cross-checked against OPQ's own event
// output), not invented here.
type IncomingMessage struct {
	IsGroup      bool
	IsTemporary  bool
	TempGroupUin uint32
	FromUin      uint32 // group code for a group message, sender uin for a friend message
	FromUid      string
	ToUin        uint32
	ToUid        string
	SenderUin    uint32
	SenderUid    string
	SenderNick   string
	GroupUin     uint32
	GroupName    string
	GroupCard    string
	MsgSeq       uint32
	MsgTime      uint32
	MsgUID       uint64
	Content      string
	Images       []MessageImage
	Ats          []MessageAt
	Segments     []Segment
}

// MessageImage is one image in a received message, with the fields the OPQ event reports.
type MessageImage struct {
	FileID   uint32
	FileMd5  []byte // raw; the event reports it base64-encoded
	FileSize uint32
	Width    uint32
	Height   uint32
	URL      string
}

// MessageAt is one @mention in a received message.
type MessageAt struct {
	Uin  uint32
	Nick string
}

// MessageEvent is a decoded incoming message paired with the account it arrived on. The bot
// broadcasts these to subscribers; each protocol adapter (OPQ, OneBot) formats its own wire
// shape from it, so the decode happens once.
type MessageEvent struct {
	SelfUin uint32
	// Exactly one of Msg or Notice is set: Msg for a chat message, Notice for a membership
	// change, recall, mute, admin change or poke.
	Msg    *IncomingMessage
	Notice *Notice
}

// DecodePush decodes the body of an olpush MsgPush frame. A frame is either a chat message or a
// notice (a membership change, recall, mute, admin change or poke); exactly one of the returned
// values is non-nil for a frame this understands, and both are nil for one it does not (an
// unhandled sub-type), which the caller skips.
//
// The body opens with the same four-byte SSO length every reply body carries, so it is stripped
// first; the rest is the PushMsg protobuf. The ContentHead.Type decides message vs notice.
func DecodePush(body []byte) (*IncomingMessage, *Notice, error) {
	body = stripSSOBodyLength(body)
	top, err := EveryField(body)
	if err != nil {
		return nil, nil, fmt.Errorf("protocol: push: %w", err)
	}
	pushBody, ok := FirstField(top, 1) // PushMsg.message = PushMsgBody
	if !ok || !pushBody.IsData {
		return nil, nil, fmt.Errorf("protocol: push carries no message body")
	}
	pmb, err := EveryField(pushBody.Data)
	if err != nil {
		return nil, nil, fmt.Errorf("protocol: push body: %w", err)
	}

	// ContentHead (field 2): Type(1) selects message vs notice, SubType(2) narrows a notice.
	var chType, chSub uint32
	if ch, ok := FirstField(pmb, 2); ok && ch.IsData {
		if chf, err := EveryField(ch.Data); err == nil {
			chType = pbU32(chf, 1)
			chSub = pbU32(chf, 2)
		}
	}
	if isNoticeType(chType) {
		// Body (field 3) → MsgContent (field 2) is the notice's own bytes.
		var content []byte
		if b, ok := FirstField(pmb, 3); ok && b.IsData {
			if bf, err := EveryField(b.Data); err == nil {
				content = pbBytes(bf, 2)
			}
		}
		n := decodeNotice(chType, chSub, content)
		if n != nil && n.MsgTime == 0 {
			f, _ := EveryField(pbBytes(pmb, 2))
			n.MsgTime = pbU32(f, 6)
		}
		return nil, n, nil
	}
	return decodeMessageBody(pmb), nil, nil
}

// DecodePushMessage decodes a push frame as a chat message, returning nil for a notice frame.
// It is kept for callers (and tests) that only care about chat messages.
func DecodePushMessage(body []byte) (*IncomingMessage, error) {
	m, _, err := DecodePush(body)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, fmt.Errorf("protocol: push is not a chat message")
	}
	return m, nil
}

// decodeMessageBody turns the PushMsgBody fields of a chat message into an IncomingMessage.
func decodeMessageBody(pmb []Field) *IncomingMessage {
	m := &IncomingMessage{}

	// ResponseHead (field 1): FromUin(1)=sender, FromUid(2), ToUin(5), ToUid(6), Grp(8).
	if rh, ok := FirstField(pmb, 1); ok && rh.IsData {
		head, err := EveryField(rh.Data)
		if err == nil {
			m.SenderUin = pbU32(head, 1)
			m.FromUid = pbStr(head, 2)
			m.SenderUid = m.FromUid
			m.ToUin = pbU32(head, 5)
			m.ToUid = pbStr(head, 6)
			fw, _ := EveryField(pbBytes(head, 7))
			m.SenderNick = pbStr(fw, 6)
			m.TempGroupUin = pbU32(fw, 5)
			// ResponseGrp (field 8): GroupUin(1), MemberName(4), GroupName(7).
			if grp, ok := FirstField(head, 8); ok && grp.IsData {
				m.IsGroup = true
				g, err := EveryField(grp.Data)
				if err == nil {
					m.GroupUin = pbU32(g, 1)
					m.GroupCard = pbStr(g, 4)
					m.GroupName = pbStr(g, 7)
				}
			}
		}
	}

	// ContentHead (field 2): Sequence(5), TimeStamp(6), NewId(12 = message uid).
	if ch, ok := FirstField(pmb, 2); ok && ch.IsData {
		head, err := EveryField(ch.Data)
		if err == nil {
			m.IsTemporary = pbU32(head, 1) == 141
			m.MsgSeq = pbU32(head, 5)
			m.MsgTime = pbU32(head, 6)
			m.MsgUID = pbU64(head, 12)
		}
	}
	if m.IsTemporary {
		if m.TempGroupUin == 0 {
			m.TempGroupUin = m.GroupUin
		}
		m.IsGroup, m.GroupUin, m.GroupCard = false, 0, ""
	} else {
		m.TempGroupUin = 0
	}

	// Body (field 3) → RichText (field 1) → Elems (field 2, repeated) → Elem.Text(1).Str(1).
	if b, ok := FirstField(pmb, 3); ok && b.IsData {
		bf, err := EveryField(b.Data)
		if err == nil {
			if rt, ok := FirstField(bf, 1); ok && rt.IsData {
				rtf, err := EveryField(rt.Data)
				if err == nil {
					for _, elem := range FieldsWith(rtf, 2) {
						if !elem.IsData {
							continue
						}
						ef, err := EveryField(elem.Data)
						if err != nil {
							continue
						}
						if t, ok := FirstField(ef, 1); ok && t.IsData { // Text element (text or @)
							tf, err := EveryField(t.Data)
							if err == nil {
								if at, ok := parseAt(tf); ok {
									m.Ats = append(m.Ats, at)
								}
								m.Content += pbStr(tf, 1)
							}
						}
						// NotOnlineImage (field 4): the common group/friend image element.
						if img, ok := FirstField(ef, 4); ok && img.IsData {
							if f, err := EveryField(img.Data); err == nil {
								m.Images = append(m.Images, MessageImage{
									FileID:   pbU32(f, 23),
									FileSize: pbU32(f, 2),
									FileMd5:  pbBytes(f, 7),
									Height:   pbU32(f, 8),
									Width:    pbU32(f, 9),
									URL:      imageURL(pbStr(f, 15), pbStr(f, 14)),
								})
							}
						}
						// CustomFace (field 8): the other image element shape.
						if img, ok := FirstField(ef, 8); ok && img.IsData {
							if f, err := EveryField(img.Data); err == nil {
								m.Images = append(m.Images, MessageImage{
									FileID:   pbU32(f, 7),
									FileSize: pbU32(f, 25),
									FileMd5:  pbBytes(f, 13),
									Height:   pbU32(f, 23),
									Width:    pbU32(f, 22),
									URL:      imageURL(pbStr(f, 16), pbStr(f, 15)),
								})
							}
						}
						// CommonElem (field 53): the modern NT rich-media element. Service
						// type 48 carries an image as an NTV2RichMedia MsgInfo.
						if ce, ok := FirstField(ef, 53); ok && ce.IsData {
							if cf, err := EveryField(ce.Data); err == nil && pbU32(cf, 1) == 48 && (pbU32(cf, 3) == 10 || pbU32(cf, 3) == 20 || pbU32(cf, 3) == 0) {
								if img, ok := decodeRichMediaImage(pbBytes(cf, 2)); ok {
									m.Images = append(m.Images, img)
								}
							}
						}
					}
				}
			}
		}
	}

	m.Segments = contentSegments(pmb, m.GroupUin)

	// OPQ reports FromUin as the group code for a group message, the sender for a friend one.
	if m.IsGroup {
		m.FromUin = m.GroupUin
	} else {
		m.FromUin = m.SenderUin
	}
	return m
}

func pbU32(fields []Field, tag int) uint32 {
	if f, ok := FirstField(fields, tag); ok && !f.IsData {
		return uint32(f.Number)
	}
	return 0
}

func pbU64(fields []Field, tag int) uint64 {
	if f, ok := FirstField(fields, tag); ok && !f.IsData {
		return f.Number
	}
	return 0
}

func pbStr(fields []Field, tag int) string {
	if f, ok := FirstField(fields, tag); ok && f.IsData {
		return string(f.Data)
	}
	return ""
}

func pbBytes(fields []Field, tag int) []byte {
	if f, ok := FirstField(fields, tag); ok && f.IsData {
		return f.Data
	}
	return nil
}

// imageURL picks the usable image URL. The element may carry a full URL or just a path; an NT
// image path is served from the multimedia host.
func imageURL(orig, big string) string {
	u := orig
	if u == "" {
		u = big
	}
	if u == "" {
		return ""
	}
	if strings.HasPrefix(u, "/") {
		return "https://multimedia.nt.qq.com.cn" + u
	}
	return u
}

// decodeRichMediaImage decodes a CommonElem service-type-48 payload (an NTV2RichMedia MsgInfo)
// into an image: MsgInfo → MsgInfoBody[0] → IndexNode.FileInfo (size, md5, dimensions) and
// PictureInfo (the URL). The server supplies the URL ready to use in the push.
func decodeRichMediaImage(pbElem []byte) (MessageImage, bool) {
	info, err := EveryField(pbElem)
	if err != nil {
		return MessageImage{}, false
	}
	bodyF, ok := FirstField(info, 1) // MsgInfoBody (first of the repeated list)
	if !ok || !bodyF.IsData {
		return MessageImage{}, false
	}
	body, err := EveryField(bodyF.Data)
	if err != nil {
		return MessageImage{}, false
	}
	var img MessageImage
	if idxF, ok := FirstField(body, 1); ok && idxF.IsData { // IndexNode
		if idx, err := EveryField(idxF.Data); err == nil {
			if fiF, ok := FirstField(idx, 1); ok && fiF.IsData { // FileInfo
				if fi, err := EveryField(fiF.Data); err == nil {
					img.FileSize = pbU32(fi, 1)
					img.FileMd5 = decodeHexMaybe(pbStr(fi, 2)) // FileHash
					img.Width = pbU32(fi, 6)
					img.Height = pbU32(fi, 7)
				}
			}
		}
	}
	if picF, ok := FirstField(body, 2); ok && picF.IsData { // PictureInfo
		if pic, err := EveryField(picF.Data); err == nil {
			img.URL = buildRichMediaURL(pbStr(pic, 3), pbStr(pic, 1)) // Domain, UrlPath
		}
	}
	if len(img.FileMd5) == 0 && img.URL == "" {
		return MessageImage{}, false
	}
	return img, true
}

// decodeHexMaybe turns a 32-char hex md5 string into its raw bytes; anything else is kept as is.
func decodeHexMaybe(s string) []byte {
	if len(s) == 32 {
		if b, err := hex.DecodeString(s); err == nil {
			return b
		}
	}
	return []byte(s)
}

// buildRichMediaURL assembles the image URL from the picture info's domain and path.
func buildRichMediaURL(domain, path string) string {
	if path == "" {
		return ""
	}
	if domain != "" {
		if strings.HasPrefix(domain, "http") {
			return domain + path
		}
		return "https://" + domain + path
	}
	if strings.HasPrefix(path, "/") {
		return "https://multimedia.nt.qq.com.cn" + path
	}
	return path
}

// parseAt reads an @mention out of a Text element: the classic attr-6 buffer is a fixed prefix
// followed by the target uin. A Text element without it is ordinary text.
func parseAt(textFields []Field) (MessageAt, bool) {
	r, _ := EveryField(pbBytes(textFields, 12))
	if pbU32(r, 3) == 1 {
		return MessageAt{Uin: 0, Nick: "all"}, true
	}
	if pbU32(r, 3) == 2 && pbU32(r, 4) != 0 {
		return MessageAt{Uin: pbU32(r, 4), Nick: strings.TrimPrefix(pbStr(textFields, 1), "@")}, true
	}
	buf := pbBytes(textFields, 3) // Attr6Buf
	if len(buf) >= 11 {
		if uin := binary.BigEndian.Uint32(buf[7:11]); uin != 0 {
			return MessageAt{Uin: uin, Nick: strings.TrimPrefix(pbStr(textFields, 1), "@")}, true
		}
	}
	return MessageAt{}, false
}
