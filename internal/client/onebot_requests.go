package client

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"openopq/internal/protocol"
)

type pendingRequest struct {
	Flag, UID, SubType, Comment string
	Kind                        protocol.NoticeKind
	Group, Event                uint32
	Sequence                    uint64
	Filtered                    bool
	Created                     time.Time
	Decision                    string
	Done                        bool
	Approved                    bool
	Busy                        bool
}
type requestRegistry struct {
	mu      sync.Mutex
	entries map[string]*pendingRequest
}

func (r *requestRegistry) add(p pendingRequest) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.entries == nil {
		r.entries = map[string]*pendingRequest{}
	}
	now := time.Now()
	for k, v := range r.entries {
		if now.Sub(v.Created) > 24*time.Hour {
			delete(r.entries, k)
		}
	}
	for _, v := range r.entries {
		if v.Kind == p.Kind && v.UID == p.UID && v.Group == p.Group && v.Sequence == p.Sequence && v.Event == p.Event && v.Filtered == p.Filtered {
			return v.Flag
		}
	}
	if len(r.entries) >= 1024 {
		var oldest string
		var t time.Time
		for k, v := range r.entries {
			if oldest == "" || v.Created.Before(t) {
				oldest, t = k, v.Created
			}
		}
		delete(r.entries, oldest)
	}
	token := make([]byte, 24)
	if _, e := rand.Read(token); e != nil {
		return ""
	}
	p.Flag = hex.EncodeToString(token)
	p.Created = now
	r.entries[p.Flag] = &p
	return p.Flag
}
func (b *Bot) resolveRequestUID(ctx context.Context, uid string) (uint32, error) {
	if uid == "" {
		return 0, fmt.Errorf("request has no applicant UID")
	}
	b.mu.Lock()
	uin := b.byUID[uid]
	b.mu.Unlock()
	if uin != 0 {
		return uin, nil
	}
	data := protocol.Marshal(protocol.StringField(1, uid), protocol.VarintField(2, 0), protocol.BytesField(3, protocol.Marshal(protocol.VarintField(1, 20002))))
	f, e := b.obQuery(ctx, protocol.OidbCommand(0xfe1, 2), data, false)
	if e != nil {
		return 0, e
	}
	body := obFields(obBytes(f, 1))
	uin = obNumber(body, 3)
	if uin == 0 {
		return 0, fmt.Errorf("QQ did not resolve request applicant")
	}
	b.remember(uin, uid, "")
	return uin, nil
}
func (b *Bot) groupRequests(ctx context.Context) ([]pendingRequest, error) {
	out := []pendingRequest{}
	for _, filtered := range []bool{false, true} {
		sub := uint32(1)
		if filtered {
			sub = 2
		}
		f, e := b.obQuery(ctx, protocol.OidbCommand(0x10c0, sub), protocol.Marshal(protocol.VarintField(1, 100), protocol.VarintField(2, 0)), false)
		if e != nil {
			return nil, e
		}
		for _, v := range protocol.FieldsWith(f, 1) {
			r := obFields(v.Data)
			if obNumber(r, 3) != 1 {
				continue
			}
			event := obNumber(r, 2)
			if event != 1 && event != 2 && event != 22 {
				continue
			}
			seq, _ := protocol.FirstField(r, 1)
			group := obNumber(obFields(obBytes(r, 4)), 1)
			uid := obString(obFields(obBytes(r, 5)), 1)
			if event == 2 {
				uid = obString(obFields(obBytes(r, 6)), 1)
				if uid == "" {
					uid = obString(obFields(obBytes(r, 5)), 1)
				}
			}
			if group == 0 || uid == "" || seq.Number == 0 {
				continue
			}
			subtype := "add"
			if event == 2 {
				subtype = "invite"
			}
			p := pendingRequest{Kind: protocol.NoticeGroupRequest, UID: uid, Group: group, Event: event, Sequence: seq.Number, Filtered: filtered, SubType: subtype, Comment: obString(r, 10)}
			p.Flag = b.obRequests.add(p)
			out = append(out, p)
		}
	}
	return out, nil
}

func (b *Bot) oneBotGroupRequests(ctx context.Context, ignoredOnly bool) (any, error) {
	r, e := b.groupRequests(ctx)
	if e != nil {
		return nil, e
	}
	invited, join := []map[string]any{}, []map[string]any{}
	for _, p := range r {
		if ignoredOnly && !p.Filtered {
			continue
		}
		uin, e := b.resolveRequestUID(ctx, p.UID)
		if e != nil {
			return nil, e
		}
		m := map[string]any{"request_id": p.Sequence, "group_id": p.Group, "user_id": uin, "comment": p.Comment, "flag": p.Flag, "sub_type": p.SubType, "checked": false, "actor": 0}
		if p.SubType == "invite" {
			invited = append(invited, m)
		} else {
			join = append(join, m)
		}
	}
	out := map[string]any{"invited_requests": invited, "join_requests": join}
	if ignoredOnly {
		out["InvitedRequest"] = invited
	}
	return out, nil
}

func (b *Bot) prepareRequest(ctx context.Context, n *protocol.Notice) error {
	if n.Kind == protocol.NoticeGroupRequest {
		requests, e := b.groupRequests(ctx)
		if e != nil {
			return e
		}
		var match *pendingRequest
		for i := range requests {
			r := &requests[i]
			if r.UID == n.UserUid && r.Group == n.GroupUin && r.Event == n.RequestEvent {
				if match != nil {
					return fmt.Errorf("ambiguous group request; refusing to choose an unrelated sequence")
				}
				match = r
			}
		}
		if match == nil {
			return fmt.Errorf("no matching pending server request")
		}
		n.Flag, n.Comment = match.Flag, match.Comment
	} else {
		n.Flag = b.obRequests.add(pendingRequest{Kind: n.Kind, UID: n.UserUid, Comment: n.Comment, Sequence: uint64(n.MsgTime)})
	}
	uin, e := b.resolveRequestUID(ctx, n.UserUid)
	if e != nil {
		return e
	}
	n.UserUin = uin
	return nil
}
func (b *Bot) requestAction(ctx context.Context, action string, raw json.RawMessage) (any, error) {
	if action == "get_group_system_msg" {
		return b.oneBotGroupRequests(ctx, false)
	}
	if action == "get_group_ignore_add_request" || action == "get_group_ignored_notices" {
		return b.oneBotGroupRequests(ctx, true)
	}
	var p struct {
		Flag    string `json:"flag"`
		Approve *bool  `json:"approve"`
		SubType string `json:"sub_type"`
		Type    string `json:"type"`
		Reason  string `json:"reason"`
		Remark  string `json:"remark"`
	}
	if e := json.Unmarshal(raw, &p); e != nil {
		return nil, e
	}
	if p.Flag == "" {
		return nil, fmt.Errorf("flag required")
	}
	if p.SubType == "" {
		p.SubType = p.Type
	}
	approve := p.Approve == nil || *p.Approve
	if len(p.Reason) > 1024 || len(p.Remark) > 128 {
		return nil, fmt.Errorf("request text is too long")
	}
	decision := fmt.Sprintf("%t:%s:%s", approve, p.Reason, p.Remark)
	r := &b.obRequests
	r.mu.Lock()
	v := r.entries[p.Flag]
	if v == nil || time.Since(v.Created) > 24*time.Hour {
		r.mu.Unlock()
		return nil, fmt.Errorf("unknown or expired flag for this account")
	}
	group := action == "set_group_add_request"
	if (v.Kind == protocol.NoticeGroupRequest) != group || group && (p.SubType != v.SubType) {
		r.mu.Unlock()
		return nil, fmt.Errorf("request type does not match flag")
	}
	if v.Busy {
		r.mu.Unlock()
		return nil, fmt.Errorf("request is being processed")
	}
	v.Busy = true
	defer func() { r.mu.Lock(); v.Busy = false; r.mu.Unlock() }()
	if v.Decision != "" {
		done, same, approved := v.Done, v.Decision == decision, v.Approved
		r.mu.Unlock()
		if done && same {
			return nil, nil
		}
		if approved && same && p.Remark != "" {
			err := b.setRequestRemark(ctx, v.UID, p.Remark)
			if err == nil {
				r.mu.Lock()
				v.Done = true
				r.mu.Unlock()
			}
			return nil, err
		}
		return nil, fmt.Errorf("request already attempted; outcome cannot be retried blindly")
	}
	v.Decision = decision
	copy := *v
	r.mu.Unlock()
	var body []byte
	var command string
	if group {
		accept := uint64(1)
		if !approve {
			accept = 2
		}
		args := []protocol.Field{protocol.VarintField(1, copy.Sequence), protocol.VarintField(2, uint64(copy.Event)), protocol.VarintField(3, uint64(copy.Group))}
		if !approve && p.Reason != "" {
			args = append(args, protocol.StringField(4, p.Reason))
		}
		sub := uint32(1)
		if copy.Filtered {
			sub = 2
		}
		command = protocol.OidbCommand(0x10c8, sub)
		body = protocol.Marshal(protocol.VarintField(1, accept), protocol.BytesField(2, protocol.Marshal(args...)))
	} else {
		accept := uint64(3)
		if !approve {
			accept = 5
		}
		command = protocol.OidbCommand(0xb5d, 44)
		body = protocol.Marshal(protocol.VarintField(1, accept), protocol.StringField(2, copy.UID))
	}
	_, err := b.obQuery(ctx, command, body, false)
	r.mu.Lock()
	v.Approved = err == nil
	v.Done = err == nil && (!approve || group || p.Remark == "")
	r.mu.Unlock()
	if err == nil && approve && !group && p.Remark != "" {
		err = b.setRequestRemark(ctx, copy.UID, p.Remark)
		r.mu.Lock()
		v.Done = err == nil
		r.mu.Unlock()
	}
	return nil, err
}

func (b *Bot) setRequestRemark(ctx context.Context, uid, remark string) error {
	uin, e := b.resolveRequestUID(ctx, uid)
	if e != nil {
		return e
	}
	ex, e := b.service()
	if e != nil {
		return e
	}
	if _, e = ex.Exchange(ctx, "ProfileService.ChangeFriendName", protocol.BuildFriendRemark(uin, remark)); e != nil {
		return fmt.Errorf("friend approved, remark update failed: %w", e)
	}
	friends, e := b.oneBotFriends(ctx)
	if e != nil {
		return fmt.Errorf("friend approved, cannot verify remark: %w", e)
	}
	for _, f := range friends {
		if f["user_id"] == uin && f["remark"] == remark {
			return nil
		}
	}
	return fmt.Errorf("friend approved, server has not confirmed the requested remark")
}
