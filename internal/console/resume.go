package console

import (
	"context"
	"errors"
	"fmt"
	"time"
)

func suspendManaged(b ManagedBot) error {
	if suspend, ok := b.(interface{ Suspend() error }); ok {
		return suspend.Suspend()
	}
	return b.Close()
}
func pauseManaged(b ManagedBot) error {
	if pause, ok := b.(interface{ PauseLogin() error }); ok {
		return pause.PauseLogin()
	}
	return nil
}

func (s *Server) selectAccountForResume(text string) error {
	p, created, err := s.opt.Store.GetOrCreate(text)
	if err != nil {
		return err
	}
	guid := p.GUID
	if s.opt.LoginStore != nil {
		if all, listErr := s.opt.LoginStore.List(); listErr == nil {
			for _, m := range all {
				if m.Account == p.Account && m.InvalidReason == "" {
					guid = m.GUID
					if m.AutoResume {
						break
					}
				}
			}
		}
	}
	if err := s.activate(guid, p.Account); err != nil {
		return err
	}
	s.opt.Logger.Info("account device selected for restore", "account", p.Account, "guid", guid, "computer_name", p.ComputerName, "new_device", created)
	return nil
}

func (s *Server) Resume(account string) error {
	if !s.op.TryLock() {
		return errors.New("a login is already running; cancel it first")
	}
	if s.Snapshot().Online {
		s.op.Unlock()
		return errors.New("account is already online")
	}
	if err := s.selectAccountForResume(account); err != nil {
		s.op.Unlock()
		return err
	}
	s.mu.RLock()
	b, guid := s.active, s.state.GUID
	s.mu.RUnlock()
	restore, ok := b.(interface{ ResumeLogin(context.Context) error })
	if !ok {
		s.op.Unlock()
		return errors.New("saved QQ login not supported")
	}
	ctx, cancel := context.WithTimeout(s.root, 3*time.Minute)
	s.mu.Lock()
	s.cancel = cancel
	s.state.Phase = "restoring"
	s.state.Error = ""
	s.state.Notice = "正在使用本机保存的登录票据恢复，无需扫码。"
	s.mu.Unlock()
	go func() {
		defer s.op.Unlock()
		defer cancel()
		defer func() { s.mu.Lock(); s.cancel = nil; s.mu.Unlock() }()
		if err := restore.ResumeLogin(ctx); err != nil {
			s.loginError(err)
			return
		}
		if ctx.Err() != nil {
			s.loginError(ctx.Err())
			return
		}
		if err := s.acceptOnline(ctx, b, account, guid); err != nil {
			s.loginError(err)
		}
	}()
	return nil
}

func (s *Server) ForgetLogin() error {
	return s.disconnect(true)
}

func (h *Hub) ResumeAccount(account string) error {
	// Reuse the initially empty legacy slot before adding further account tabs.
	h.mu.Lock()
	var existing *slot
	for _, s := range h.slots {
		if s.account == account {
			existing = s
			break
		}
	}
	h.mu.Unlock()
	if existing == nil {
		if s := h.defaultSlot(); s != nil && s.server != nil && s.server.Snapshot().Phase == "idle" {
			if err := h.Select(account); err != nil {
				return err
			}
			existing = s
		}
	}
	if existing == nil {
		id, err := h.Add(account)
		if err != nil {
			return err
		}
		existing = h.find(id)
	}
	if existing.server == nil {
		return errors.New("account belongs to an attached console")
	}
	if err := h.claim(existing.id, account); err != nil {
		return err
	}
	if err := existing.server.Resume(account); err != nil {
		h.release(existing.id)
		return err
	}
	return nil
}

func (h *Hub) ResumeSavedLogins(onlyAccount string) error {
	if h.options.LoginStore == nil {
		return nil
	}
	metadata, listErr := h.options.LoginStore.List()
	profiles, err := h.options.Store.List()
	if err != nil {
		return err
	}
	identities := map[string]string{}
	for _, p := range profiles {
		identities[p.Account] = p.GUID
	}
	failures := []error{listErr}
	for _, m := range metadata {
		if !m.AutoResume || m.InvalidReason != "" || (onlyAccount != "" && m.Account != onlyAccount) {
			continue
		}
		if identities[m.Account] != "" && identities[m.Account] != m.GUID {
			h.options.Logger.Warn("saved QQ login device differs from account profile; restoring the saved device", "account", m.Account)
		}
		if err := h.ResumeAccount(m.Account); err != nil {
			failures = append(failures, fmt.Errorf("restore account %s: %w", m.Account, err))
		}
	}
	return errors.Join(failures...)
}
