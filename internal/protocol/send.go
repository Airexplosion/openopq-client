package protocol

import (
	"fmt"
	"strconv"
	"strings"
)

func itoa(v uint32) string { return strconv.FormatUint(uint64(v), 10) }

// SendMessageCommand is the service an outgoing message is addressed to.
const SendMessageCommand = "MessageSvc.PbSendMsg"

// SendTarget distinguishes group recipients, ordinary C2C peers, and private
// temporary peers with a source group. NT private sends also need the peer UID.
type SendTarget struct {
	Group     uint32 // group code; non-zero means a group message
	Uin       uint32 // friend uin (when Group is zero)
	Uid       string // friend uid
	TempGroup uint32 // source group for private temporary sessions; never a group recipient
}

// IsGroup reports whether the target is a group.
func (t SendTarget) IsGroup() bool { return t.Group != 0 }

func (t SendTarget) IsTemporary() bool { return t.TempGroup != 0 }

// BuildSendRouting keeps the three recipient kinds mutually exclusive. QQ's
// temporary route is slot 3, with source group at tag 3 and peer UID at tag 4.
func BuildSendRouting(t SendTarget) []byte {
	if t.IsGroup() {
		return Marshal(BytesField(2, Marshal(VarintField(1, uint64(t.Group)))))
	}
	if t.IsTemporary() {
		return Marshal(BytesField(3, Marshal(VarintField(3, uint64(t.TempGroup)), StringField(4, t.Uid))))
	}
	c2c := []Field{VarintField(1, uint64(t.Uin))}
	if t.Uid != "" {
		c2c = append(c2c, StringField(2, t.Uid))
	}
	return Marshal(BytesField(1, Marshal(c2c...)))
}

func sendContentHead(t SendTarget) []byte {
	command := uint64(0)
	if t.IsTemporary() {
		command = 11
	}
	return Marshal(VarintField(1, 1), VarintField(2, 0), VarintField(3, command))
}

// SendAt is one @mention to include in an outgoing message. Uid is the modern account id the
// NT server binds the mention to; it is resolved from a received message when only the uin is
// known.
type SendAt struct {
	Uin  uint32
	Nick string
	Uid  string
}

// atElement builds a Text element that mentions a member in the NT shape: a Text whose Str is
// the displayed "@name" and whose PbReserve (field 12) carries a MentionExtra naming the target
// by uid. The pre-NT attr-6 buffer the 7.0.x server no longer accepts — a message carrying it
// is taken (result=0) but silently not delivered — so the mention travels in PbReserve instead.
func atElement(at SendAt) []byte {
	display := at.Nick
	if display == "" {
		display = "@" + itoa(at.Uin)
	} else if !strings.HasPrefix(display, "@") {
		display = "@" + display
	}
	// MentionExtra{ Type(3): 1=@all else 2, Uin(4), Field5(5)=0, Uid(9) }. Mirrors LagrangeGo's
	// AtElement: the uid in field 9 is what the server reads; the uin is carried too but the
	// NT render/notify binds on the uid.
	typ := uint64(2)
	if at.Uin == 0 {
		typ = 1 // @everyone
	}
	extra := []Field{VarintField(3, typ), VarintField(4, uint64(at.Uin)), VarintField(5, 0)}
	if at.Uid != "" {
		extra = append(extra, StringField(9, at.Uid))
	}
	reserve := Marshal(extra...)
	text := Marshal(StringField(1, display+" "), BytesField(12, reserve))
	return Marshal(BytesField(1, text)) // Elem{ Text }
}

// BuildSendMessage builds the MessageSvc.PbSendMsg request body for a plain-text message.
//
// The shape is the server's own Message protobuf: RoutingHead(1), ContentHead(2), Body(3),
// ClientSequence(4), Random(5) and, for a group or temporary peer, a Control(12). The body is one RichText with
// a single Text element. clientSeq, random and msgFlag are supplied so a packet is reproducible.
func BuildSendMessage(t SendTarget, text string, ats []SendAt, clientSeq, random, msgFlag uint32) []byte {
	routing := BuildSendRouting(t)

	// ContentHead{ Type(1)=1, SubType(2)=0, DivSeq(3)=0 }
	contentHead := sendContentHead(t)

	// Body{ RichText(1){ Elems(2)[ @elems…, text elem ] } }: the @ mentions come first, then
	// a Text element carrying the content.
	var elems []Field
	for _, at := range ats {
		elems = append(elems, BytesField(2, atElement(at)))
	}
	if text != "" || len(ats) == 0 {
		textElem := Marshal(BytesField(1, Marshal(StringField(1, text))))
		elems = append(elems, BytesField(2, textElem))
	}
	richText := Marshal(elems...)
	body := Marshal(BytesField(1, richText))

	fields := []Field{
		BytesField(1, routing),
		BytesField(2, contentHead),
		BytesField(3, body),
		VarintField(4, uint64(clientSeq)),
		VarintField(5, uint64(random)),
	}
	if t.IsGroup() || t.IsTemporary() {
		// MessageControl(12){ MsgFlag(1) } — a group message carries it.
		fields = append(fields, BytesField(12, Marshal(VarintField(1, uint64(msgFlag)))))
	}
	return Marshal(fields...)
}

// TextElem builds the bytes of a plain Text element (an Elem), for use as a media message's
// caption alongside an image or voice element.
func TextElem(text string) []byte {
	return Marshal(BytesField(1, Marshal(StringField(1, text))))
}

// BuildMediaMessage builds a PbSendMsg whose RichText carries pre-built elements (each the
// bytes of one Elem), for an image or voice message. It mirrors BuildSendMessage's envelope but
// takes the elements ready-made, since a media element is assembled from an upload's result.
func BuildMediaMessage(t SendTarget, elems [][]byte, clientSeq, random, msgFlag uint32) []byte {
	routing := BuildSendRouting(t)
	contentHead := sendContentHead(t)
	var elemFields []Field
	for _, e := range elems {
		elemFields = append(elemFields, BytesField(2, e))
	}
	body := Marshal(BytesField(1, Marshal(elemFields...))) // Body{ RichText{ Elems } }
	fields := []Field{
		BytesField(1, routing), BytesField(2, contentHead), BytesField(3, body),
		VarintField(4, uint64(clientSeq)), VarintField(5, uint64(random)),
	}
	if t.IsGroup() || t.IsTemporary() {
		fields = append(fields, BytesField(12, Marshal(VarintField(1, uint64(msgFlag)))))
	}
	return Marshal(fields...)
}

// SendResult is what a successful send reports back.
type SendResult struct {
	Sequence  uint32
	Timestamp uint32
	// Required to recall an outgoing private message; never guessed from sequence.
	ClientSequence uint32
	Random         uint32
}

// ParseSendResponse reads the PbSendMsg reply, which opens with the usual four-byte SSO body
// length. A non-zero result is a refusal and carries a message. Temporary
// sends require the private sequence instead of accepting a missing receipt.
func ParseSendResponse(answer []byte, temporary ...bool) (SendResult, error) {
	answer = stripSSOBodyLength(answer)
	fields, err := EveryField(answer)
	if err != nil {
		return SendResult{}, fmt.Errorf("protocol: send response: %w", err)
	}
	if result := pbU32(fields, 1); result != 0 {
		return SendResult{}, fmt.Errorf("protocol: send refused: result=%d %s", result, pbStr(fields, 2))
	}
	seq := pbU32(fields, 11) // GroupSequence
	if len(temporary) > 0 && temporary[0] {
		seq = pbU32(fields, 14)
		if seq == 0 {
			return SendResult{}, fmt.Errorf("protocol: temporary private send returned no acknowledged private sequence")
		}
	}
	if seq == 0 {
		seq = pbU32(fields, 14) // PrivateSequence
	}
	return SendResult{Sequence: seq, Timestamp: pbU32(fields, 3)}, nil
}

// SendSegment is prevalidated ordered content. Bytes are consumed for image and voice.
type SendSegment struct {
	Type, Text string
	At         SendAt
	Bytes      []byte
	Face       uint32
	Reply      *SendReply
	Duration   uint32 // voice duration in seconds
}

// SendReply refers to a real message in this account's current conversation cache.
type SendReply struct {
	Sequence, SenderUin, Timestamp uint32
	SenderUid                      string
	Elements                       [][]byte
}

// ReplyElem encodes QQ's Elem.SrcMsg. Field numbers follow the QQ message schema
// documented by LagrangeGo (client/packets/pb/message/element.proto).
func ReplyElem(r SendReply) []byte {
	fields := []Field{VarintField(1, uint64(r.Sequence)), VarintField(2, uint64(r.SenderUin)), VarintField(3, uint64(r.Timestamp))}
	for _, e := range r.Elements {
		fields = append(fields, BytesField(5, e))
	}
	reserve := []Field{VarintField(3, uint64(r.Sequence))}
	if r.SenderUid != "" {
		reserve = append(reserve, StringField(7, r.SenderUid))
	}
	fields = append(fields, BytesField(8, Marshal(reserve...)), VarintField(10, 0))
	return Marshal(BytesField(45, Marshal(fields...)))
}

func AtElem(at SendAt) []byte { return atElement(at) }
