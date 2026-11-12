package client

import (
	"context"
	"fmt"
	"sync"
	"time"

	"openopq/internal/protocol"
)

type temporaryRoute struct {
	UID      string
	Incoming bool
	Until    time.Time
}
type temporaryRoutes struct {
	mu      sync.Mutex
	owner   uint32
	entries map[[2]uint32]temporaryRoute // peer + source group, private to this Bot
}

func (b *Bot) temporaryOwner() uint32 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.account.Uin
}

func (r *temporaryRoutes) prune(owner uint32) {
	if r.owner != owner || r.entries == nil {
		r.entries = map[[2]uint32]temporaryRoute{}
		r.owner = owner
	}
	for key, entry := range r.entries {
		if !time.Now().Before(entry.Until) {
			delete(r.entries, key)
		}
	}
}
func (r *temporaryRoutes) put(owner, peer, group uint32, uid string, incoming bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.prune(owner)
	key := [2]uint32{peer, group}
	if old, ok := r.entries[key]; ok {
		incoming = incoming || old.Incoming
	}
	if _, exists := r.entries[key]; !exists && len(r.entries) >= 1024 {
		var oldest [2]uint32
		var until time.Time
		for k, v := range r.entries {
			if until.IsZero() || v.Until.Before(until) {
				oldest, until = k, v.Until
			}
		}
		delete(r.entries, oldest)
	}
	ttl := time.Minute
	if incoming {
		ttl = 24 * time.Hour
	}
	r.entries[key] = temporaryRoute{UID: uid, Incoming: incoming, Until: time.Now().Add(ttl)}
}

// Only decoded incoming temporary messages create automatic reply hints.
// A QQ number learned from a group is not evidence of a temporary session.
func (b *Bot) observeTemporaryMessage(m *protocol.IncomingMessage) {
	if m.IsGroup || m.SenderUin == 0 {
		return
	}
	self := b.temporaryOwner()
	peer, uid := m.SenderUin, m.SenderUid
	if peer == self {
		peer, uid = m.ToUin, m.ToUid
	}
	if peer == 0 {
		return
	}
	if m.IsTemporary {
		b.tempRoutes.put(self, peer, m.TempGroupUin, uid, true)
		return
	}
	// An ordinary incoming private message supersedes implicit temporary hints;
	// explicit source-group routes remain available.
	r := &b.tempRoutes
	r.mu.Lock()
	defer r.mu.Unlock()
	r.prune(self)
	for key, entry := range r.entries {
		if key[0] == peer {
			entry.Incoming = false
			r.entries[key] = entry
		}
	}
}

// PrepareSendTarget is also called by the API before fetching or uploading media.
// Explicit temporary sends verify the exact peer/source-group pair, rather than
// guessing a group from the account-wide contact cache.
func (b *Bot) PrepareSendTarget(ctx context.Context, target protocol.SendTarget) (protocol.SendTarget, error) {
	if err := ctx.Err(); err != nil {
		return target, err
	}
	if target.IsGroup() {
		if target.IsTemporary() {
			return target, fmt.Errorf("group and temporary private targets cannot be combined")
		}
		return target, nil
	}
	if target.Uin == 0 {
		return target, fmt.Errorf("private user_id is required")
	}
	self := b.temporaryOwner()
	r := &b.tempRoutes
	r.mu.Lock()
	r.prune(self)
	if target.TempGroup == 0 {
		groups := map[uint32]bool{}
		for key, entry := range r.entries {
			if key[0] == target.Uin && entry.Incoming {
				groups[key[1]] = true
			}
		}
		if len(groups) > 1 || groups[0] {
			r.mu.Unlock()
			return target, fmt.Errorf("temporary private conversation needs an explicit source group_id")
		}
		for group := range groups {
			target.TempGroup = group
		}
	}
	entry, exists := r.entries[[2]uint32{target.Uin, target.TempGroup}]
	r.mu.Unlock()
	if !target.IsTemporary() {
		if target.Uid == "" {
			target.Uid = b.uidFor(target.Uin)
		}
		return target, nil
	}
	if exists && entry.UID != "" {
		if b.temporaryOwner() != self {
			return target, fmt.Errorf("account changed while preparing temporary private recipient")
		}
		if target.Uid != "" && target.Uid != entry.UID {
			return target, fmt.Errorf("temporary recipient UID does not match this account's source-group context")
		}
		target.Uid = entry.UID
		return target, nil
	}
	members, err := b.oneBotMembers(ctx, target.TempGroup)
	if err != nil {
		return target, fmt.Errorf("resolve temporary recipient in group %d: %w", target.TempGroup, err)
	}
	if b.temporaryOwner() != self {
		return target, fmt.Errorf("account changed while resolving temporary private recipient")
	}
	var peerUID string
	selfPresent := false
	for _, member := range members {
		id, _ := member["user_id"].(uint32)
		if id == self && self != 0 {
			selfPresent = true
		}
		if id == target.Uin {
			peerUID = b.uidFor(id)
		}
	}
	if !selfPresent || peerUID == "" {
		return target, fmt.Errorf("temporary private send requires both this account and the recipient in group %d", target.TempGroup)
	}
	if target.Uid != "" && target.Uid != peerUID {
		return target, fmt.Errorf("temporary recipient UID does not match the source-group member")
	}
	target.Uid = peerUID
	r.put(self, target.Uin, target.TempGroup, peerUID, false)
	return target, nil
}
