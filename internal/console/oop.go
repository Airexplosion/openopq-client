package console

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"openopq/internal/buildinfo"
	"openopq/internal/device"
	"openopq/internal/protocol"
)

func (s *Server) oopSetting(account string) (string, error) {
	id, err := device.Account(account)
	if err != nil || id == "" || s.opt.TransportDir == "" {
		return "", fmt.Errorf("需要先选择已配置的账号")
	}
	return filepath.Join(s.opt.TransportDir, "oop", id+".disabled"), nil
}

func (s *Server) oopEnabled(account string) bool {
	path, err := s.oopSetting(account)
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return errors.Is(err, os.ErrNotExist)
}

func (s *Server) setOOP(account string, enabled bool) error {
	path, err := s.oopSetting(account)
	if err != nil {
		return err
	}
	if enabled {
		err = os.Remove(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return os.WriteFile(path, nil, 0600)
}

func (s *Server) oopControl(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Enabled *bool `json:"enabled"`
	}
	if err := strictJSON(w, r, &input); err != nil || input.Enabled == nil {
		failure(w, 400, fmt.Errorf("需要布尔值 enabled"))
		return
	}
	s.mu.RLock()
	account := s.state.Account
	s.mu.RUnlock()
	if err := s.setOOP(account, *input.Enabled); err != nil {
		failure(w, 409, err)
		return
	}
	send(w, 200, map[string]bool{"enabled": *input.Enabled})
}

func (s *Server) oopCommand(ctx context.Context, b ManagedBot, e protocol.MessageEvent) {
	m := e.Msg
	cmd := strings.TrimSpace(m.Content)
	if cmd != "#oop" && cmd != "#oop off" && cmd != "#oop on" {
		return
	}
	for _, seg := range m.Segments {
		if seg.Type != "text" {
			return
		}
	}
	if e.SelfUin == 0 || m.SenderUin == 0 {
		return
	}
	account := strconv.FormatUint(uint64(e.SelfUin), 10)
	target := protocol.SendTarget{Group: m.GroupUin}
	if !m.IsGroup {
		target = protocol.SendTarget{Uin: m.SenderUin, Uid: m.SenderUid, TempGroup: m.TempGroupUin}
		if m.SenderUin == e.SelfUin && m.ToUin != 0 {
			target.Uin = m.ToUin
			target.Uid = m.ToUid
		}
	} else if target.Group == 0 {
		return
	}
	var reply string
	if cmd == "#oop" {
		if !s.oopEnabled(account) {
			return
		}
		s.mu.RLock()
		elapsed := int(time.Since(s.state.StartedAt).Minutes())
		s.mu.RUnlock()
		platform := runtime.GOOS
		switch platform {
		case "windows":
			platform = "Windows"
		case "darwin":
			platform = "macOS"
		case "linux":
			platform = "Linux"
		}
		reply = fmt.Sprintf("OpenOPQ 状态\n版本：%s\n平台：%s / %s\n运行时长：%d天%d小时%d分钟", buildinfo.Version, platform, runtime.GOARCH, elapsed/1440, elapsed/60%24, elapsed%60)
	} else {
		if m.SenderUin != e.SelfUin {
			return
		}
		enabled := cmd == "#oop on"
		if err := s.setOOP(account, enabled); err != nil {
			s.opt.Logger.Warn("save #oop setting", "error", err)
			return
		}
		reply = "已关闭 #oop 状态回复"
		if enabled {
			reply = "已开启 #oop 状态回复"
		}
	}
	sender, ok := b.(interface {
		SendMessage(context.Context, protocol.SendTarget, string, []protocol.SendAt) (protocol.SendResult, error)
	})
	if !ok {
		return
	}
	sendCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := sender.SendMessage(sendCtx, target, reply, nil); err != nil {
		s.opt.Logger.Warn("send #oop reply", "error", err)
	}
}
