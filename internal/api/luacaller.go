package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

const (
	defaultCallTimeout = 15 * time.Second
	maxCallTimeout     = 120 * time.Second
)

// handleClusterInfo describes the framework instance.
func (s *Server) handleClusterInfo(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), defaultCallTimeout)
	defer cancel()
	if s.opts.ClusterInfo != nil {
		value, err := s.opts.ClusterInfo(ctx)
		if err != nil {
			writeBase(w, RetError, err.Error())
			return
		}
		writeData(w, value)
		return
	}

	self, err := s.bot.Self(ctx)
	if err != nil {
		s.logger.Error("cluster info failed", "error", err)
		writeBase(w, RetError, err.Error())
		return
	}
	writeData(w, map[string]any{
		"Uin":      self.Uin,
		"Uid":      self.UID,
		"NickName": self.Nickname,
		"Online":   self.Online,
	})
}

// handleLuaAPICaller is the dispatcher almost every framework operation goes through:
// the caller names a function and the framework runs it.
//
//	/v1/LuaApiCaller?qq=<uin>&funcname=<name>&timeout=<seconds>
func (s *Server) handleLuaAPICaller(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	function := query.Get("funcname")
	if function == "" {
		writeBase(w, RetError, "funcname is required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), callTimeout(query.Get("timeout")))
	defer cancel()
	if function == "MagicCgiCmd" {
		var call struct {
			CgiCmd     string
			CgiRequest json.RawMessage
		}
		if err := json.Unmarshal(readBody(r), &call); err != nil || call.CgiCmd == "" || len(call.CgiRequest) == 0 || string(call.CgiRequest) == "null" || !json.Valid(call.CgiRequest) || call.CgiRequest[0] != '{' {
			writeBase(w, RetError, "CgiCmd and an object CgiRequest are required")
			return
		}
		var value any
		var err error
		switch call.CgiCmd {
		case "MessageSvc.PbSendMsg":
			if err = validateOPQText(call.CgiRequest); err == nil {
				value, err = s.handleSend(ctx, call.CgiRequest)
			}
		case "GetGroupLists":
			groups, e := s.bot.Groups(ctx)
			err = e
			items := make([]map[string]any, 0, len(groups))
			for _, g := range groups {
				items = append(items, map[string]any{"GroupCode": g.GroupUin, "GroupName": g.GroupName, "MemberCnt": g.MemberCount})
			}
			value = map[string]any{"GroupLists": items}
		case "ClusterInfo":
			if s.opts.ClusterInfo != nil {
				value, err = s.opts.ClusterInfo(ctx)
			} else {
				self, e := s.bot.Self(ctx)
				err = e
				users := []map[string]any{}
				if self.Online {
					users = append(users, map[string]any{"QQ": fmt.Sprint(self.Uin), "NickName": self.Nickname})
				}
				value = map[string]any{"QQUsers": users, "QQUsersCounts": len(users)}
			}
		default:
			err = fmt.Errorf("unsupported CgiCmd: %s", call.CgiCmd)
		}
		if err != nil {
			writeBase(w, RetError, err.Error())
			return
		}
		writeData(w, value)
		return
	}

	// Sending a message goes through a dedicated path (it is not an OIDB operation); every
	// other funcname falls through to the generic protocol caller.
	if isSendFunc(function) {
		result, err := s.handleSend(ctx, requestParams(r))
		if err != nil {
			s.logger.Error("send failed", "funcname", function, "error", err)
			writeBase(w, RetError, err.Error())
			return
		}
		writeData(w, result)
		return
	}

	result, err := s.bot.Call(ctx, function, requestParams(r))
	if err != nil {
		s.logger.Error("api call failed", "funcname", function, "error", err)
		writeBase(w, RetError, err.Error())
		return
	}
	writeData(w, result)
}

// callTimeout clamps a caller-supplied timeout: callers may ask for less, never more
// than the framework is willing to spend on one operation.
func callTimeout(requested string) time.Duration {
	if requested == "" {
		return defaultCallTimeout
	}
	seconds, err := strconv.Atoi(requested)
	if err != nil || seconds <= 0 {
		return defaultCallTimeout
	}
	timeout := time.Duration(seconds) * time.Second
	if timeout > maxCallTimeout {
		return maxCallTimeout
	}
	return timeout
}
