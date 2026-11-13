package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"openopq/internal/protocol"
)

// When the original push predates startup, preserve the quote's embedded preview
// so get_msg still gives QQ Agent useful context. Do not invent the sender's name.
func (o *oneBotRuntime) rememberQuote(target protocol.SendTarget, r protocol.SendReply) int32 {
	key := oneBotMessageKey(target, r.SenderUin, r.Sequence, r.Timestamp)
	o.mu.Lock()
	id, exists := o.keys[key]
	current := o.cache[id]
	o.mu.Unlock()
	if exists && time.Since(current.Created) < time.Hour {
		return id // Keep the complete event and sender profile, not just its quote preview.
	}
	var text strings.Builder
	for _, elem := range r.Elements {
		fields, _ := protocol.EveryField(elem)
		if field, ok := protocol.FirstField(fields, 1); ok && field.IsData {
			f, _ := protocol.EveryField(field.Data)
			if v, ok := protocol.FirstField(f, 1); ok && v.IsData {
				text.Write(v.Data)
			}
		} else {
			text.WriteString("[附件]")
		}
	}
	kind := "private"
	if target.Group != 0 {
		kind = "group"
	}
	message := any(cqEscape(text.String()))
	if o.format() == "array" {
		message = cqSegments(cqEscape(text.String()))
	}
	data := map[string]any{"time": r.Timestamp, "message_type": kind, "sender": map[string]any{"user_id": r.SenderUin}, "message": message, "raw_message": cqEscape(text.String())}
	if target.Group != 0 {
		data["group_id"] = target.Group
	} else {
		data["user_id"] = r.SenderUin
	}
	return o.remember(obCachedMessage{Key: key, Target: target, Result: protocol.SendResult{Sequence: r.Sequence, Timestamp: r.Timestamp}, Data: data, Reply: &r})
}

func (s *Server) cachedReply(id int32, target protocol.SendTarget) (*protocol.SendReply, error) {
	s.ob.mu.Lock()
	m, ok := s.ob.cache[id]
	s.ob.mu.Unlock()
	if !ok || time.Since(m.Created) > time.Hour || m.Reply == nil {
		return nil, fmt.Errorf("reply message expired or unavailable in current account cache")
	}
	if m.Target.Group != target.Group || m.Target.TempGroup != target.TempGroup || (target.Group == 0 && m.Target.Uin != target.Uin) {
		return nil, fmt.Errorf("reply message belongs to another conversation")
	}
	r := *m.Reply
	return &r, nil
}

// QQ Agent sends message_id; standard OneBot callers may supply resource id.
// Never confuse an unavailable integer message id with a QQ resource id.
func (s *Server) forwardForAgent(ctx context.Context, act obAction) obResponse {
	var p struct {
		ID        string          `json:"id"`
		MessageID json.RawMessage `json:"message_id"`
	}
	if json.Unmarshal(act.Params, &p) != nil {
		return obBad("invalid forward parameters", act.Echo)
	}
	if p.ID == "" {
		// Non-numeric string message_id is a resource-id alias used by v11 clients.
		var resource string
		if json.Unmarshal(p.MessageID, &resource) == nil && resource != "" {
			if _, err := strconv.ParseInt(resource, 10, 64); err != nil {
				p.ID = resource
			}
		}
	}
	if p.ID == "" {
		var n int32
		if json.Unmarshal(p.MessageID, &n) != nil {
			var str string
			if json.Unmarshal(p.MessageID, &str) == nil {
				v, err := strconv.ParseInt(str, 10, 32)
				if err == nil {
					n = int32(v)
				}
			}
		}
		if n <= 0 {
			return obBad("id or positive message_id required", act.Echo)
		}
		s.ob.mu.Lock()
		m, ok := s.ob.cache[n]
		s.ob.mu.Unlock()
		if !ok || time.Since(m.Created) > time.Hour {
			return obFail("forward message expired or unavailable", act.Echo)
		}
		// Both string and array message formats are supported by reusing CQ serialization.
		cq, _ := m.Data["raw_message"].(string)
		if str, ok := m.Data["message"].(string); ok {
			cq = str
		} else {
			raw, _ := json.Marshal(m.Data["message"])
			var segs []struct {
				Type string         `json:"type"`
				Data map[string]any `json:"data"`
			}
			if json.Unmarshal(raw, &segs) == nil {
				for _, seg := range segs {
					if seg.Type == "forward" {
						p.ID, _ = seg.Data["id"].(string)
						break
					}
				}
			}
		}
		if cq != "" {
			for _, seg := range cqSegments(cq) {
				if seg["type"] == "forward" {
					p.ID = seg["data"].(map[string]string)["id"]
					break
				}
			}
		}
		if p.ID == "" {
			return obBad("message contains no forward resource", act.Echo)
		}
	}
	act.Params, _ = json.Marshal(map[string]string{"id": p.ID})
	response := s.backendOneBotAction(ctx, act)
	if response.Status == "ok" {
		if data, ok := response.Data.(map[string]any); ok {
			if nodes, ok := data["message"]; ok {
				data["messages"] = nodes
			} else if nodes, ok := data["messages"]; ok {
				data["message"] = nodes
			}
		}
	}
	return response
}
