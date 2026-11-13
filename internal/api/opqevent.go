package api

import (
	"encoding/base64"
	"encoding/json"

	"openopq/internal/protocol"
)

// This file formats an incoming message as the original OPQBot `/ws` event JSON — the shape was
// read off a running OPQBot's websocket, so a client written for OPQBot consumes openopq's `/ws`
// unchanged.
// Temporary messages expose the known source GroupCode in a sparse compatibility
// header; this does not reproduce unobserved legacy signatures/header fields.

type opqGroupInfo struct {
	GroupCard    string `json:"GroupCard"`
	GroupCode    uint32 `json:"GroupCode"`
	GroupInfoSeq int    `json:"GroupInfoSeq"`
	GroupLevel   int    `json:"GroupLevel"`
	GroupRank    int    `json:"GroupRank"`
	GroupType    int    `json:"GroupType"`
	GroupName    string `json:"GroupName"`
}

type opqMsgHead struct {
	FromUin            uint32        `json:"FromUin"`
	FromUid            string        `json:"FromUid"`
	ToUin              uint32        `json:"ToUin"`
	ToUid              string        `json:"ToUid"`
	FromType           int           `json:"FromType"`
	SenderUin          uint32        `json:"SenderUin"`
	SenderUid          string        `json:"SenderUid"`
	SenderNick         string        `json:"SenderNick"`
	MsgType            int           `json:"MsgType"`
	C2cCmd             int           `json:"C2cCmd"`
	MsgSeq             uint32        `json:"MsgSeq"`
	MsgTime            uint32        `json:"MsgTime"`
	MsgRandom          uint32        `json:"MsgRandom"`
	MsgUid             uint64        `json:"MsgUid"`
	GroupInfo          *opqGroupInfo `json:"GroupInfo"`
	C2CTempMessageHead any           `json:"C2CTempMessageHead"`
}

type opqImage struct {
	FileId   uint32 `json:"FileId"`
	FileMd5  string `json:"FileMd5"`
	FileSize uint32 `json:"FileSize"`
	Height   uint32 `json:"Height"`
	Url      string `json:"Url"`
	Width    uint32 `json:"Width"`
}

type opqAt struct {
	Uin  uint32 `json:"Uin"`
	Nick string `json:"Nick"`
}

type opqMsgBody struct {
	SubMsgType int        `json:"SubMsgType"`
	Content    string     `json:"Content"`
	ContentRaw string     `json:"ContentRaw"`
	AtUinLists []opqAt    `json:"AtUinLists"`
	Images     []opqImage `json:"Images"`
	Video      any        `json:"Video"`
	Voice      any        `json:"Voice"`
	File       any        `json:"File"`
	RedBag     any        `json:"RedBag"`
}

type opqEventData struct {
	MsgHead opqMsgHead  `json:"MsgHead"`
	MsgBody *opqMsgBody `json:"MsgBody"`
	Event   any         `json:"Event"`
}

type opqPacket struct {
	EventData opqEventData `json:"EventData"`
	EventName string       `json:"EventName"`
}

type opqEvent struct {
	CurrentPacket opqPacket `json:"CurrentPacket"`
	CurrentQQ     uint32    `json:"CurrentQQ"`
}

// opqEventJSON formats an event as the OPQ ws event JSON, or returns nil for an event this
// adapter does not render.
func opqEventJSON(ev protocol.MessageEvent) []byte {
	if ev.Notice != nil {
		return opqNoticeJSON(ev)
	}
	if ev.Msg == nil {
		return nil
	}
	m := ev.Msg
	head := opqMsgHead{
		FromUin: m.FromUin, FromUid: m.FromUid, ToUin: m.ToUin, ToUid: m.ToUid,
		SenderUin: m.SenderUin, SenderUid: m.SenderUid, SenderNick: m.SenderNick,
		MsgSeq: m.MsgSeq, MsgTime: m.MsgTime, MsgRandom: m.MsgRandom, MsgUid: m.MsgUID,
		C2CTempMessageHead: nil,
	}
	body := &opqMsgBody{Content: m.Content, ContentRaw: m.Content}
	for _, at := range m.Ats {
		body.AtUinLists = append(body.AtUinLists, opqAt{Uin: at.Uin, Nick: at.Nick})
	}
	for _, img := range m.Images {
		body.Images = append(body.Images, opqImage{
			FileId: img.FileID, FileMd5: base64.StdEncoding.EncodeToString(img.FileMd5),
			FileSize: img.FileSize, Height: img.Height, Url: img.URL, Width: img.Width,
		})
	}
	if len(body.Images) > 0 {
		body.SubMsgType = 48
	}
	var name string
	if m.IsGroup {
		head.FromType, head.MsgType, head.C2cCmd = 2, 82, 0
		head.GroupInfo = &opqGroupInfo{
			GroupCard: m.GroupCard, GroupCode: m.GroupUin, GroupName: m.GroupName,
			GroupType: 1, GroupLevel: 1, GroupRank: 1,
		}
		name = "ON_EVENT_GROUP_NEW_MSG"
	} else {
		head.FromType, head.MsgType, head.C2cCmd = 1, 166, 11
		name = "ON_EVENT_FRIEND_NEW_MSG"
		if m.IsTemporary {
			head.FromType, head.MsgType = 3, 141
			if m.TempGroupUin != 0 {
				head.C2CTempMessageHead = map[string]any{"GroupCode": m.TempGroupUin}
			}
		}
	}
	out, _ := json.Marshal(opqEvent{
		CurrentPacket: opqPacket{EventData: opqEventData{MsgHead: head, MsgBody: body}, EventName: name},
		CurrentQQ:     ev.SelfUin,
	})
	return out
}

// opqNoticeJSON formats a notice as an OPQ ws event. The EventName follows OPQBot's notice
// naming; the decoded fields travel in the Event object (people are named by both uin — when the
// bot has seen them — and uid). The names are a reasonable match for an OPQBot client; a
// consumer that needs an exact legacy shape should prefer the OneBot adapter's standard notices.
func opqNoticeJSON(ev protocol.MessageEvent) []byte {
	n := ev.Notice
	head := opqMsgHead{MsgTime: n.MsgTime, C2CTempMessageHead: nil}
	var name string
	event := map[string]any{}
	group := func() {
		head.FromType, head.FromUin = 2, n.GroupUin
		head.GroupInfo = &opqGroupInfo{GroupCode: n.GroupUin, GroupType: 1, GroupLevel: 1, GroupRank: 1}
	}
	switch n.Kind {
	case protocol.NoticeGroupJoin:
		name = "ON_EVENT_GROUP_JOIN"
		group()
		event = map[string]any{"UserID": n.UserUin, "UserUid": n.UserUid,
			"InviterUin": n.OperatorUin, "InviterUid": n.OperatorUid, "JoinType": n.IncreaseType}
	case protocol.NoticeGroupLeave:
		name = "ON_EVENT_GROUP_EXIT"
		group()
		event = map[string]any{"UserID": n.UserUin, "UserUid": n.UserUid,
			"OperatorUin": n.OperatorUin, "OperatorUid": n.OperatorUid, "ExitType": n.ExitType}
	case protocol.NoticeGroupAdmin:
		name = "ON_EVENT_GROUP_ADMIN"
		group()
		event = map[string]any{"UserID": n.UserUin, "UserUid": n.UserUid, "Flag": boolToInt(n.Enable)}
	case protocol.NoticeGroupRecall:
		name = "ON_EVENT_GROUP_REVOKE"
		group()
		event = map[string]any{"UserID": n.UserUin, "UserUid": n.UserUid,
			"OperatorUin": n.OperatorUin, "OperatorUid": n.OperatorUid,
			"MsgSeq": n.MsgSeq, "MsgRandom": n.MsgRandom}
	case protocol.NoticeGroupMute:
		name = "ON_EVENT_GROUP_SHUT"
		group()
		event = map[string]any{"UserID": n.UserUin, "UserUid": n.UserUid,
			"OperatorUin": n.OperatorUin, "OperatorUid": n.OperatorUid, "ShutTime": n.Duration}
	case protocol.NoticeGroupPoke:
		name = "ON_EVENT_GROUP_POKE"
		group()
		event = map[string]any{"UserID": n.UserUin, "Target": n.Target, "Action": n.Action, "Suffix": n.Suffix}
	case protocol.NoticeFriendRecall:
		name = "ON_EVENT_FRIEND_REVOKE"
		head.FromType, head.FromUin, head.SenderUin, head.SenderUid = 1, n.UserUin, n.UserUin, n.UserUid
		event = map[string]any{"UserID": n.UserUin, "UserUid": n.UserUid, "MsgSeq": n.MsgSeq, "MsgRandom": n.MsgRandom}
	case protocol.NoticeFriendPoke:
		name = "ON_EVENT_FRIEND_POKE"
		head.FromType, head.FromUin, head.SenderUin = 1, n.UserUin, n.UserUin
		event = map[string]any{"UserID": n.UserUin, "Target": n.Target, "Action": n.Action, "Suffix": n.Suffix}
	default:
		return nil
	}
	out, _ := json.Marshal(opqEvent{
		CurrentPacket: opqPacket{EventData: opqEventData{MsgHead: head, Event: event}, EventName: name},
		CurrentQQ:     ev.SelfUin,
	})
	return out
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
