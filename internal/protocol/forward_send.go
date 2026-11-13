package protocol

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
)

// SendForwardCommand uploads a multi-message payload and returns a resid.
const SendForwardCommand = "trpc.group.long_msg_interface.MsgService.SsoSendLongMsg"

// ForwardNode is one node of a forwarded chat record. Elements are already-packed
// Elems (the same bytes SendRichMessage would put on the wire).
type ForwardNode struct {
	SenderUin  uint32
	SenderUid  string
	SenderName string
	Time       uint32
	Sequence   uint32
	Elements   [][]byte
}

// PackForwardPush builds one PushMsgBody for a forward node. Field numbers follow
// message.proto (ResponseHead, ContentHead, Body) as decodeMessageBody reads them.
func PackForwardPush(n ForwardNode, groupUin uint32) []byte {
	head := []Field{VarintField(1, uint64(n.SenderUin))}
	if n.SenderUid != "" {
		head = append(head, StringField(2, n.SenderUid))
	}
	if n.SenderName != "" {
		head = append(head, BytesField(7, Marshal(StringField(6, n.SenderName))))
	}
	contentType := uint64(166)
	if groupUin != 0 {
		contentType = 82
		grp := []Field{VarintField(1, uint64(groupUin))}
		if n.SenderName != "" {
			grp = append(grp, StringField(4, n.SenderName))
		}
		head = append(head, VarintField(3, 82), BytesField(8, Marshal(grp...)))
	} else {
		head = append(head, VarintField(3, 166))
	}
	content := []Field{VarintField(1, contentType)}
	if n.Sequence != 0 {
		content = append(content, VarintField(5, uint64(n.Sequence)))
	}
	if n.Time != 0 {
		content = append(content, VarintField(6, uint64(n.Time)))
	}
	elems := n.Elements
	if len(elems) == 0 {
		elems = [][]byte{TextElem("")}
	}
	var rich []Field
	for _, e := range elems {
		rich = append(rich, BytesField(2, e))
	}
	return Marshal(
		BytesField(1, Marshal(head...)),
		BytesField(2, Marshal(content...)),
		BytesField(3, Marshal(BytesField(1, Marshal(rich...)))),
	)
}

// BuildSendForward uploads the gzip'd LongMsgResult LagrangeGo sends as SsoSendLongMsg.
// Type is 3 for a group and 1 for a friend; the uid slot is the group code or the
// account's own uid (LongMsgUid.Uid is field 2).
func BuildSendForward(selfUID string, groupUin uint32, nodes []ForwardNode) []byte {
	var bodies []Field
	for _, n := range nodes {
		bodies = append(bodies, BytesField(1, PackForwardPush(n, groupUin)))
	}
	result := Marshal(BytesField(2, Marshal(
		StringField(1, "MultiMsg"),
		BytesField(2, Marshal(bodies...)),
	)))
	typ := uint64(1)
	uid := selfUID
	if groupUin != 0 {
		typ = 3
		uid = strconv.FormatUint(uint64(groupUin), 10)
	}
	info := Marshal(
		VarintField(1, typ),
		BytesField(2, Marshal(StringField(2, uid))),
		VarintField(3, uint64(groupUin)),
		BytesField(4, gzipBytes(result)),
	)
	settings := Marshal(
		VarintField(1, 4), VarintField(2, 1), VarintField(3, 7), VarintField(4, 0),
	)
	return Marshal(BytesField(2, info), BytesField(15, settings))
}

// ParseSendForward reads SendLongMsgResp.Result(2).ResId(3).
func ParseSendForward(answer []byte) (string, error) {
	f, err := EveryField(stripSSOBodyLength(answer))
	if err != nil {
		return "", fmt.Errorf("protocol: send forward: %w", err)
	}
	result, err := EveryField(pbBytes(f, 2))
	if err != nil {
		return "", fmt.Errorf("protocol: send forward: %w", err)
	}
	id := pbStr(result, 3)
	if id == "" {
		return "", fmt.Errorf("protocol: send forward: empty resid")
	}
	return id, nil
}

// LightAppElem builds Elem.LightAppElem (field 51): zlib-compressed JSON with a 0x01 prefix.
func LightAppElem(content string) []byte {
	return Marshal(BytesField(51, Marshal(BytesField(1, append([]byte{0x01}, zlibBytes([]byte(content))...)))))
}

type forwardLightApp struct {
	App    string `json:"app"`
	Config struct {
		Autosize int64  `json:"autosize"`
		Forward  int64  `json:"forward"`
		Round    int64  `json:"round"`
		Type     string `json:"type"`
		Width    int64  `json:"width"`
	} `json:"config"`
	Desc  string `json:"desc"`
	Extra string `json:"extra"`
	Meta  struct {
		Detail struct {
			News []struct {
				Text string `json:"text"`
			} `json:"news"`
			Resid   string `json:"resid"`
			Source  string `json:"source"`
			Summary string `json:"summary"`
			UniSeq  string `json:"uniseq"`
		} `json:"detail"`
	} `json:"meta"`
	Prompt string `json:"prompt"`
	Ver    string `json:"ver"`
	View   string `json:"view"`
}

// ForwardLightAppElem is the com.tencent.multimsg light-app LagrangeGo sends after a resid.
func ForwardLightAppElem(resid string, nodes []ForwardNode) []byte {
	fileID := randomHex(16)
	extra, _ := json.Marshal(map[string]any{"filename": fileID, "tsum": len(nodes)})
	news := make([]struct {
		Text string `json:"text"`
	}, 0, len(nodes))
	source := ""
	seen := map[string]bool{}
	for _, n := range nodes {
		name := n.SenderName
		if name == "" {
			name = strconv.FormatUint(uint64(n.SenderUin), 10)
		}
		preview := elemPreview(n.Elements)
		if preview == "" {
			preview = "转发消息"
		}
		news = append(news, struct {
			Text string `json:"text"`
		}{Text: name + ": " + preview})
		if !seen[name] {
			seen[name] = true
			if source == "" {
				source = name
			} else {
				source += "和" + name
			}
		}
	}
	if len(news) == 0 {
		news = []struct {
			Text string `json:"text"`
		}{{Text: "转发消息"}}
	}
	if len(news) > 4 {
		news = news[:4]
	}
	if source == "" {
		source = "群聊的聊天记录"
	} else {
		source += "的聊天记录"
	}
	card := forwardLightApp{
		App:    "com.tencent.multimsg",
		Desc:   "[聊天记录]",
		Extra:  string(extra),
		Prompt: "[聊天记录]",
		Ver:    "0.0.0.5",
		View:   "contact",
	}
	card.Config.Autosize = 1
	card.Config.Forward = 1
	card.Config.Round = 1
	card.Config.Type = "normal"
	card.Config.Width = 300
	card.Meta.Detail.News = news
	card.Meta.Detail.Resid = resid
	card.Meta.Detail.Source = source
	card.Meta.Detail.Summary = fmt.Sprintf("查看%d条转发消息", len(nodes))
	card.Meta.Detail.UniSeq = fileID
	raw, _ := json.Marshal(card)
	return LightAppElem(string(raw))
}

func elemPreview(elems [][]byte) string {
	var b bytes.Buffer
	for _, e := range elems {
		f, err := EveryField(e)
		if err != nil {
			continue
		}
		if t := pbBytes(f, 1); len(t) > 0 {
			tf, _ := EveryField(t)
			b.WriteString(pbStr(tf, 1))
		}
		if b.Len() > 50 {
			break
		}
	}
	s := b.String()
	if len(s) > 50 {
		return s[:50]
	}
	return s
}

func gzipBytes(p []byte) []byte {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	_, _ = w.Write(p)
	_ = w.Close()
	return buf.Bytes()
}

func zlibBytes(p []byte) []byte {
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	_, _ = w.Write(p)
	_ = w.Close()
	return buf.Bytes()
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatUint(uint64(len(b)), 16)
	}
	return hex.EncodeToString(b)
}
