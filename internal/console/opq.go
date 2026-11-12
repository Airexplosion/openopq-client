package console

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"openopq/internal/client"
	"runtime"
	"strconv"
	"strings"

	"openopq/internal/bot"
	"openopq/internal/buildinfo"
	"openopq/internal/protocol"
)

func opqReply(w http.ResponseWriter, value any, err error) {
	ret, message := 0, ""
	if err != nil {
		ret, message, value = -1, err.Error(), nil
	}
	send(w, http.StatusOK, map[string]any{"CgiBaseResponse": map[string]any{"Ret": ret, "ErrMsg": message}, "ResponseData": value, "Data": nil})
}

func (h *Hub) clusterInfo(context.Context) (any, error) {
	users := []map[string]any{}
	for _, s := range h.states() {
		if s.Online {
			users = append(users, map[string]any{"QQ": s.Account, "NickName": s.Nickname, "ReceiveCount": s.Messages})
		}
	}
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	return map[string]any{"QQUsers": users, "QQUsersCounts": len(users), "Version": buildinfo.Version, "Platform": runtime.GOOS, "GoArch": runtime.GOARCH, "GoVersion": runtime.Version(), "CpuNum": runtime.NumCPU(), "GoroutineNum": runtime.NumGoroutine(), "Alloc": fmt.Sprint(memory.Alloc), "Sys": fmt.Sprint(memory.Sys), "TotalAlloc": fmt.Sprint(memory.TotalAlloc)}, nil
}

// OPQ selects the sending account by qq, never by device GUID or recipient UIN.
// A supplied but invalid/unknown selector must never fall back to another bot.
func (h *Hub) routeOPQ(w http.ResponseWriter, r *http.Request, id *string) bool {
	path := r.URL.Path
	if path == "/v1/clusterinfo" && r.Method == http.MethodGet {
		value, err := h.clusterInfo(r.Context())
		opqReply(w, value, err)
		return true
	}
	if path != "/v1/LuaApiCaller" && path != "/v1/login/getqrcode" && path != "/v1/login/loadsession" && path != "/v2/login/loadsession" && path != "/ws" {
		return false
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		opqReply(w, nil, fmt.Errorf("invalid query"))
		return true
	}
	values, present := query["qq"]
	if !present {
		if path == "/v1/LuaApiCaller" && *id == "" {
			opqReply(w, nil, fmt.Errorf("qq is required"))
			return true
		}
		return false
	}
	if len(values) != 1 {
		opqReply(w, nil, fmt.Errorf("exactly one qq is required"))
		return true
	}
	uin, err := strconv.ParseUint(values[0], 10, 32)
	if err != nil || uin == 0 || fmt.Sprint(uin) != values[0] {
		opqReply(w, nil, fmt.Errorf("invalid qq"))
		return true
	}
	h.mu.Lock()
	slots := append([]*slot(nil), h.slots...)
	h.mu.Unlock()
	var selected *slot
	for _, s := range slots {
		h.mu.Lock()
		account := s.account
		h.mu.Unlock()
		if s.server != nil {
			account = s.server.Snapshot().Account
		}
		if account == values[0] {
			if selected != nil {
				opqReply(w, nil, fmt.Errorf("qq has multiple sessions"))
				return true
			}
			selected = s
		}
	}
	if selected == nil && path == "/v1/login/getqrcode" && *id == "" {
		newID, e := h.Add(values[0])
		if e != nil {
			opqReply(w, nil, e)
			return true
		}
		selected = h.find(newID)
	}
	if selected == nil {
		opqReply(w, nil, fmt.Errorf("qq is not managed by this instance"))
		return true
	}
	if *id != "" && *id != selected.id {
		opqReply(w, nil, fmt.Errorf("qq does not match the selected session"))
		return true
	}
	*id = selected.id
	return false
}

// Attached legacy processes keep their live connections. The compatibility
// adapter uses their existing query endpoints; it never reloads a signer/device.
type attachedOPQBot struct {
	bot.Stub
	endpoint *url.URL
	queries  *client.Bot
}

func (b *attachedOPQBot) request(ctx context.Context, path string, body any, out any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(b.endpoint.String(), "/")+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("attached API returned HTTP %d", response.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(response.Body, 4*1024*1024)).Decode(out)
}
func (b *attachedOPQBot) Groups(ctx context.Context) ([]bot.Group, error) {
	var official struct {
		CgiBaseResponse struct {
			Ret    int
			ErrMsg string
		}
		ResponseData struct {
			GroupLists []struct {
				GroupCode uint32
				GroupName string
				MemberCnt int
			}
		}
	}
	err := b.request(ctx, "/v1/LuaApiCaller?funcname=MagicCgiCmd", map[string]any{"CgiCmd": "GetGroupLists", "CgiRequest": map[string]any{}}, &official)
	if err == nil && official.CgiBaseResponse.Ret == 0 && official.ResponseData.GroupLists != nil {
		groups := make([]bot.Group, 0, len(official.ResponseData.GroupLists))
		for _, g := range official.ResponseData.GroupLists {
			groups = append(groups, bot.Group{GroupUin: g.GroupCode, GroupName: g.GroupName, MemberCount: g.MemberCnt})
		}
		return groups, nil
	}
	var result struct {
		Status  string
		Message string
		Data    []struct {
			ID    uint32 `json:"group_id"`
			Name  string `json:"group_name"`
			Count int    `json:"member_count"`
		}
	}
	if err = b.request(ctx, "/onebot/v11/get_group_list", map[string]any{}, &result); err != nil {
		return nil, err
	}
	if result.Status != "ok" {
		return nil, fmt.Errorf("attached group query failed: %s", result.Message)
	}
	groups := make([]bot.Group, 0, len(result.Data))
	for _, g := range result.Data {
		groups = append(groups, bot.Group{GroupUin: g.ID, GroupName: g.Name, MemberCount: g.Count})
	}
	return groups, nil
}
func (b *attachedOPQBot) Call(ctx context.Context, function string, params json.RawMessage) (any, error) {
	var result struct {
		CgiBaseResponse struct {
			Ret    int
			ErrMsg string
		}
		ResponseData json.RawMessage
	}
	err := b.request(ctx, "/v1/LuaApiCaller?funcname="+url.QueryEscape(function), params, &result)
	if err != nil {
		return nil, err
	}
	if result.CgiBaseResponse.Ret != 0 {
		return nil, fmt.Errorf("%s", result.CgiBaseResponse.ErrMsg)
	}
	return result.ResponseData, nil
}
func (b *attachedOPQBot) SendMessage(ctx context.Context, target protocol.SendTarget, text string, ats []protocol.SendAt) (protocol.SendResult, error) {
	to, kind := target.Uin, 1
	if target.Group != 0 {
		to, kind = target.Group, 2
	}
	var result struct {
		CgiBaseResponse struct {
			Ret    int
			ErrMsg string
		}
		ResponseData struct{ MsgSeq, MsgTime uint32 }
	}
	err := b.request(ctx, "/v1/LuaApiCaller?funcname=MagicCgiCmd", map[string]any{"CgiCmd": "MessageSvc.PbSendMsg", "CgiRequest": map[string]any{"ToType": kind, "ToUin": to, "ToUid": target.Uid, "Content": text, "AtUinLists": ats}}, &result)
	if err != nil {
		return protocol.SendResult{}, err
	}
	if result.CgiBaseResponse.Ret != 0 {
		return protocol.SendResult{}, fmt.Errorf("%s", result.CgiBaseResponse.ErrMsg)
	}
	return protocol.SendResult{Sequence: result.ResponseData.MsgSeq, Timestamp: result.ResponseData.MsgTime}, nil
}
