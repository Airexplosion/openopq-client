package protocol

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Segment preserves the order of rich content instead of moving all images and
// mentions to the end. Media is private transport metadata, never JSON output.
type Segment struct {
	Type  string            `json:"type"`
	Data  map[string]string `json:"data"`
	Media *ReceivedMedia    `json:"-"`
}
type ReceivedMedia struct {
	Kind, File, Name, URL string
	Size                  uint32
	Group                 uint32
	Index                 []byte
}

func limitedInflate(data []byte, gzipFormat bool) ([]byte, error) {
	var r io.ReadCloser
	var err error
	if gzipFormat {
		r, err = gzip.NewReader(bytes.NewReader(data))
	} else {
		r, err = zlib.NewReader(bytes.NewReader(data))
	}
	if err != nil {
		return nil, err
	}
	defer r.Close()
	b, err := io.ReadAll(io.LimitReader(r, (16<<20)+1))
	if len(b) > 16<<20 {
		return nil, fmt.Errorf("expanded message exceeds 16 MiB")
	}
	return b, err
}
func cardData(b []byte) []byte {
	if len(b) == 0 {
		return nil
	}
	if b[0] == 1 {
		v, _ := limitedInflate(b[1:], false)
		return v
	}
	if b[0] == 0 {
		return b[1:]
	}
	return b
}
func forwardID(data []byte, isXML bool) string {
	if isXML {
		d := xml.NewDecoder(bytes.NewReader(data))
		for {
			t, e := d.Token()
			if e != nil {
				break
			}
			if s, ok := t.(xml.StartElement); ok {
				for _, a := range s.Attr {
					if a.Name.Local == "m_resid" {
						return a.Value
					}
				}
			}
		}
	} else {
		var v struct {
			Meta map[string]struct {
				ResID string `json:"resid"`
			} `json:"meta"`
		}
		if json.Unmarshal(data, &v) == nil {
			for _, k := range []string{"detail", "detail_1"} {
				if v.Meta[k].ResID != "" {
					return v.Meta[k].ResID
				}
			}
		}
	}
	return ""
}
func contentSegments(pmb []Field, group uint32) []Segment {
	body, _ := EveryField(pbBytes(pmb, 3))
	rt, _ := EveryField(pbBytes(body, 1))
	out := []Segment{}
	add := func(kind, key, value string) {
		out = append(out, Segment{Type: kind, Data: map[string]string{key: value}})
	}
	media := func(m ReceivedMedia) {
		m.Group = group
		if m.File == "" || strings.HasPrefix(m.File, ".") {
			return
		}

		if len(m.Index) > 0 && m.Kind == "image" && len(out) > 0 {
			at := len(out) - 1
			old := out[at]
			if old.Type == "image" && old.Data["file"] == m.File && old.Media != nil && len(old.Media.Index) == 0 {
				if m.URL == "" {
					m.URL = old.Data["url"]
				}
				out[at].Media = &m
				if m.URL != "" {
					out[at].Data["url"] = m.URL
				}
				return
			}
		}
		data := map[string]string{"file": m.File}
		if m.URL != "" {
			data["url"] = m.URL
		}
		out = append(out, Segment{Type: m.Kind, Data: data, Media: &m})
	}
	for _, f := range FieldsWith(rt, 2) {
		ef, _ := EveryField(f.Data)
		if t := pbBytes(ef, 1); t != nil {
			tf, _ := EveryField(t)
			if at, ok := parseAt(tf); ok {
				q := strconv.FormatUint(uint64(at.Uin), 10)
				if at.Uin == 0 {
					q = "all"
				}
				add("at", "qq", q)
			} else {
				add("text", "text", pbStr(tf, 1))
			}
		}
		if face := pbBytes(ef, 2); face != nil {
			ff, _ := EveryField(face)
			add("face", "id", strconv.FormatUint(uint64(pbU32(ff, 1)), 10))
		}
		for _, tag := range []int{4, 8} {
			if b := pbBytes(ef, tag); b != nil {
				im, _ := EveryField(b)
				md, size, orig, big := 7, 2, 15, 14
				if tag == 8 {
					md, size, orig, big = 13, 25, 16, 15
				}
				hash := hex.EncodeToString(pbBytes(im, md))
				if hash == "" && imageURL(pbStr(im, orig), pbStr(im, big)) != "" {
					h := sha256.Sum256([]byte(imageURL(pbStr(im, orig), pbStr(im, big))))
					hash = hex.EncodeToString(h[:])
				}
				media(ReceivedMedia{Kind: "image", File: hash + ".image", Name: hash + ".image", Size: pbU32(im, size), URL: imageURL(pbStr(im, orig), pbStr(im, big))})
			}
		}
		if ce := pbBytes(ef, 53); ce != nil {
			cf, _ := EveryField(ce)
			if pbU32(cf, 1) == 48 {
				biz := pbU32(cf, 3)
				kind := ""
				switch biz {
				case 10, 20:
					kind = "image"
				case 12, 22:
					kind = "record"
				case 11, 21:
					kind = "video"
				}
				if kind != "" {
					info, _ := EveryField(pbBytes(cf, 2))
					for _, v := range FieldsWith(info, 1) {
						b, _ := EveryField(v.Data)
						idx := pbBytes(b, 1)
						ix, _ := EveryField(idx)
						fi, _ := EveryField(pbBytes(ix, 1))
						fileType, _ := EveryField(pbBytes(fi, 5))
						if kind == "video" && pbU32(fileType, 1) != 2 {
							continue
						}
						hash := strings.ToLower(pbStr(fi, 2))
						file := hash + "." + kind
						if hash == "" {
							file = pbStr(ix, 2) + "." + kind
						}
						pic, _ := EveryField(pbBytes(b, 2))
						media(ReceivedMedia{Kind: kind, File: file, Name: pbStr(fi, 4), Size: pbU32(fi, 1), Index: idx, URL: buildRichMediaURL(pbStr(pic, 3), pbStr(pic, 1))})
					}
				}
			}
		}
		for _, tag := range []int{12, 51} {
			if b := pbBytes(ef, tag); b != nil {
				c, _ := EveryField(b)
				data := cardData(pbBytes(c, 1))
				if len(data) > 0 {
					if id := forwardID(data, tag == 12); id != "" {
						add("forward", "id", id)
					} else {
						kind := "json"
						if tag == 12 {
							kind = "xml"
						}
						add(kind, "data", string(data))
					}
				}
			}
		}
	}
	if ptt := pbBytes(rt, 4); ptt != nil {
		p, _ := EveryField(ptt)
		hash := hex.EncodeToString(pbBytes(p, 4))
		url := pbStr(p, 17)
		if url == "" {
			url = pbStr(p, 31)
		}
		uuid := pbStr(p, 3)
		if group != 0 && pbU32(p, 8) != 0 {
			uuid = pbStr(p, 18)
		}
		var idx []byte
		if uuid != "" {
			idx = Marshal(BytesField(1, Marshal(VarintField(1, uint64(pbU32(p, 6))), StringField(2, hash), StringField(4, pbStr(p, 5)), BytesField(5, Marshal(VarintField(1, 3), VarintField(4, 1))))), StringField(2, uuid))
		}
		media(ReceivedMedia{Kind: "record", File: hash + ".record", Name: pbStr(p, 5), Size: pbU32(p, 6), URL: url, Index: idx})
	}
	return out
}

const ReceiveForwardCommand = "trpc.group.long_msg_interface.MsgService.SsoRecvLongMsg"

func BuildReceiveForward(uid, id string) []byte {
	return Marshal(BytesField(1, Marshal(BytesField(1, Marshal(StringField(2, uid))), StringField(2, id), VarintField(3, 1))), BytesField(15, Marshal(VarintField(1, 2), VarintField(2, 0), VarintField(3, 0), VarintField(4, 0))))
}
func ParseReceiveForward(data []byte, id string) ([]*IncomingMessage, error) {
	f, e := EveryField(stripSSOBodyLength(data))
	if e != nil {
		return nil, e
	}
	result, _ := EveryField(pbBytes(f, 1))
	if r := pbStr(result, 3); r != "" && r != id {
		return nil, fmt.Errorf("forward resource identity mismatch")
	}
	payload, e := limitedInflate(pbBytes(result, 4), true)
	if e != nil {
		return nil, fmt.Errorf("forward payload: %w", e)
	}
	actions, e := EveryField(payload)
	if e != nil {
		return nil, e
	}
	out := []*IncomingMessage{}
	for _, a := range FieldsWith(actions, 2) {
		af, _ := EveryField(a.Data)
		if pbStr(af, 1) != "MultiMsg" {
			continue
		}
		content, e := EveryField(pbBytes(af, 2))
		if e != nil {
			return nil, e
		}
		for _, v := range FieldsWith(content, 1) {
			if len(out) >= 1000 {
				return nil, fmt.Errorf("forward exceeds 1000 nodes")
			}
			f, e := EveryField(v.Data)
			if e != nil {
				return nil, e
			}
			out = append(out, decodeMessageBody(f))
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("forward contains no message nodes")
	}
	return out, nil
}
