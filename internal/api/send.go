package api

import (
	"context"
	"encoding/json"
	"fmt"

	"openopq/internal/protocol"
)

// messageSender is the outgoing half of the message interface the client bot provides.
type messageSender interface {
	SendMessage(ctx context.Context, target protocol.SendTarget, text string, ats []protocol.SendAt) (protocol.SendResult, error)
}

func (s *Server) prepareSendTarget(ctx context.Context, target protocol.SendTarget) (protocol.SendTarget, error) {
	if backend, ok := s.bot.(interface {
		PrepareSendTarget(context.Context, protocol.SendTarget) (protocol.SendTarget, error)
	}); ok {
		return backend.PrepareSendTarget(ctx, target)
	}
	if target.IsTemporary() {
		return target, fmt.Errorf("temporary private messages require a native backend with temporary routing")
	}
	return target, nil
}

// isSendFunc names the flat legacy send aliases. MagicCgiCmd is dispatched by
// CgiCmd in luacaller.go and must not be classified as a send operation here.
func isSendFunc(name string) bool {
	switch name {
	case "SendMsg", "SendMsgV2":
		return true
	}
	return false
}

// atUser is one @mention in a send request (OPQ's AtUinLists entries).
type atUser struct {
	Uin  uint64 `json:"Uin"`
	Nick string `json:"Nick"`
}

// sendFields are the message fields, accepted both at the top level (openopq's flat form) and
// inside CgiRequest (OPQ's official MagicCgiCmd form). ToType: 1 friend, 2 group, 3 private.
type sendFields struct {
	ToType     int      `json:"ToType"`
	ToUin      uint64   `json:"ToUin"`
	ToUid      string   `json:"ToUid"`
	GroupID    uint64   `json:"GroupID"`
	GroupCode  uint64   `json:"GroupCode"`
	Content    string   `json:"Content"`
	AtUinLists []atUser `json:"AtUinLists"`
}

// sendRequest accepts either shape:
//
//	{ "ToType":2, "ToUin":123, "Content":"hi" }                         (flat)
//	{ "CgiCmd":"MessageSvc.PbSendMsg", "CgiRequest":{ ...sendFields } }  (OPQ official)
type sendRequest struct {
	sendFields
	CgiCmd     string      `json:"CgiCmd"`
	CgiRequest *sendFields `json:"CgiRequest"`
}

// Rich fields need their own encoders. Temporary text uses a distinct recipient
// route; never reinterpret unsupported content or silently change the target.
func validateOPQText(body json.RawMessage) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return err
	}
	for key := range fields {
		switch key {
		case "ToUin", "ToType", "ToUid", "GroupID", "GroupCode", "Content", "AtUinLists":
		default:
			return fmt.Errorf("unsupported OPQ message field: %s", key)
		}
	}
	return nil
}

// handleSend carries out a text/@ send and writes the OPQ-style response.
func (s *Server) handleSend(ctx context.Context, body json.RawMessage) (any, error) {
	sender, ok := s.bot.(messageSender)
	if !ok {
		return nil, fmt.Errorf("this build cannot send messages")
	}
	var req sendRequest
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			return nil, fmt.Errorf("send: bad request body: %w", err)
		}
	}
	f := req.sendFields
	if req.CgiRequest != nil { // the official MagicCgiCmd form wins when present
		f = *req.CgiRequest
	}
	if f.ToType != 0 && f.ToType != 1 && f.ToType != 2 && f.ToType != 3 {
		return nil, fmt.Errorf("unsupported ToType: %d", f.ToType)
	}
	if f.ToUin > 0xffffffff || f.GroupID > 0xffffffff || f.GroupCode > 0xffffffff {
		return nil, fmt.Errorf("invalid message recipient")
	}
	if f.Content == "" && len(f.AtUinLists) == 0 {
		return nil, fmt.Errorf("send: Content is empty")
	}

	var target protocol.SendTarget
	switch {
	case f.ToType == 3:
		group := f.GroupCode
		if group == 0 {
			group = f.GroupID
		}
		if group == 0 || f.ToUin == 0 || (f.GroupCode != 0 && f.GroupID != 0 && f.GroupCode != f.GroupID) {
			return nil, fmt.Errorf("temporary private messages require ToUin and one unambiguous GroupCode")
		}
		target = protocol.SendTarget{Uin: uint32(f.ToUin), Uid: f.ToUid, TempGroup: uint32(group)}
	case f.ToType == 2 || f.GroupID != 0:
		group := f.GroupID
		if group == 0 {
			group = f.ToUin
		}
		target.Group = uint32(group)
	default: // friend
		target.Uin = uint32(f.ToUin)
		target.Uid = f.ToUid
	}
	if f.ToType != 3 && f.GroupCode != 0 {
		return nil, fmt.Errorf("GroupCode is only supported for ToType 3 temporary private messages")
	}
	var err error
	target, err = s.prepareSendTarget(ctx, target)
	if err != nil {
		return nil, err
	}

	var ats []protocol.SendAt
	for _, a := range f.AtUinLists {
		ats = append(ats, protocol.SendAt{Uin: uint32(a.Uin), Nick: a.Nick})
	}

	res, err := sender.SendMessage(ctx, target, f.Content, ats)
	if err != nil {
		return nil, err
	}
	return map[string]any{"MsgSeq": res.Sequence, "MsgTime": res.Timestamp}, nil
}
