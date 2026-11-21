// Package console embeds the local management UI and its headless control API.
package console

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"openopq/internal/api"
	"openopq/internal/bot"
	"openopq/internal/buildinfo"
	"openopq/internal/device"
	"openopq/internal/loginstate"
	"openopq/internal/protocol"
)

//go:embed web/*
var assets embed.FS

type ManagedBot interface {
	bot.Bot
	Close() error
}
type Factory func(guid []byte, account uint32) (ManagedBot, error)
type Options struct {
	LoginStore     *loginstate.Store
	Store          *device.Store
	Factory        Factory
	SessionFactory func([]byte, uint32, *slog.Logger) (ManagedBot, error)
	OnStopped      func()
	Logger         *slog.Logger
	Logs           *LogBuffer
	API            api.Options
	Version        string
	Signer         string
	Headless       bool
	QRFile         string
	TransportDir   string // persistent per-device protocol listener settings
	SharedPort     int
	Security       *Security
}

type State struct {
	OOPEnabled   bool                  `json:"oop_enabled"`
	SavedLogin   *bot.SavedLoginStatus `json:"saved_login,omitempty"`
	Heartbeat    *bot.HeartbeatStatus  `json:"heartbeat,omitempty"`
	Reconnect    *bot.ReconnectStatus  `json:"reconnect,omitempty"`
	AppVersion   string                `json:"app_version"`
	Commit       string                `json:"commit"`
	BuiltAt      string                `json:"built_at"`
	Phase        string                `json:"phase"`
	Account      string                `json:"account"`
	GUID         string                `json:"guid"`
	ComputerName string                `json:"computer_name,omitempty"`
	Online       bool                  `json:"online"`
	Nickname     string                `json:"nickname"`
	Error        string                `json:"error,omitempty"`
	Notice       string                `json:"notice,omitempty"`
	QR           string                `json:"qr,omitempty"`
	QRExpires    *time.Time            `json:"qr_expires,omitempty"`
	StartedAt    time.Time             `json:"started_at"`
	Uptime       int64                 `json:"uptime"`
	Version      string                `json:"version"`
	OPQ          bool                  `json:"opq"`
	OneBot       bool                  `json:"onebot"`
	OneBot12     bool                  `json:"onebot12"`
	Headless     bool                  `json:"headless"`
	Messages     uint64                `json:"messages"`
	Notices      uint64                `json:"notices"`
}

type Server struct {
	op            sync.Mutex // one login flow at a time, including legacy login endpoints
	mu            sync.RWMutex
	opt           Options
	state         State
	active        ManagedBot
	api           http.Handler
	cancel        context.CancelFunc
	monitorCancel context.CancelFunc
	root          context.Context
	stop          context.CancelFunc
	static        http.Handler
}

func New(opt Options) *Server {
	ctx, cancel := context.WithCancel(context.Background())
	sub, _ := fs.Sub(assets, "web")
	s := &Server{opt: opt, root: ctx, stop: cancel, static: http.FileServer(http.FS(sub))}
	s.state = State{Phase: "idle", StartedAt: time.Now(), Version: opt.Version, OPQ: opt.API.OPQ, OneBot: opt.API.OneBot, OneBot12: opt.API.OneBot12, Headless: opt.Headless}
	s.api = api.NewServer(bot.NewStub(), opt.Logger, opt.API)
	return s
}

func (s *Server) Snapshot() State {
	s.mu.RLock()
	v, b := s.state, s.active
	s.mu.RUnlock()
	v.OOPEnabled = s.oopEnabled(v.Account)
	v.Uptime = int64(time.Since(v.StartedAt).Seconds())
	v.AppVersion, v.Commit, v.BuiltAt = buildinfo.Version, buildinfo.Commit, buildinfo.BuiltAt
	if status, ok := b.(interface{ SavedLoginStatus() bot.SavedLoginStatus }); ok {
		saved := status.SavedLoginStatus()
		v.SavedLogin = &saved
	}
	if b != nil && (v.Phase == "online" || v.Phase == "offline" || v.Phase == "reconnecting") {
		if own, err := b.Self(context.Background()); err == nil {
			v.Online = own.Online
			v.Nickname = own.Nickname
			if status, ok := b.(interface{ HeartbeatStatus() bot.HeartbeatStatus }); ok {
				h := status.HeartbeatStatus()
				v.Heartbeat = &h
				if h.ConsecutiveFailures > 0 && !h.Stale {
					v.Notice = "QQ SSO 心跳暂未确认成功，将在下一周期重试；请查看心跳日志"
				}
			}
			if own.Online {
				v.Phase = "online"
			} else {
				v.Phase = "offline"
			}
			if status, ok := b.(interface{ ReconnectStatus() bot.ReconnectStatus }); ok {
				r := status.ReconnectStatus()
				v.Reconnect = &r
				if r.Reconnecting {
					v.Phase, v.Online = "reconnecting", false
					v.Error = ""
					v.Notice = fmt.Sprintf("QQ 连接中断或心跳失活，正在第 %d 次重连；无需重新扫码", r.Attempt)
				}
				if r.RequiresLogin {
					v.Notice = "QQ 登录已失效或服务端要求下线，请断开后重新登录"
				}
			}
			if status, ok := b.(interface{ AuthenticationError() error }); ok {
				if err := status.AuthenticationError(); err != nil {
					v.Error = err.Error()
					v.Notice = "QQ 认证已失效，需要重新登录"
				}
			}
		}
	}
	if v.QRExpires != nil && time.Now().After(*v.QRExpires) && v.Phase == "waiting" {
		v.QR = ""
	}
	return v
}

// Select prepares an account before the first QR request. This does not connect.
func (s *Server) Select(account string) error {
	if !s.op.TryLock() {
		return fmt.Errorf("a login is already running")
	}
	defer s.op.Unlock()
	return s.selectAccount(account)
}

func (s *Server) selectAccount(text string) error {
	p, created, err := s.opt.Store.GetOrCreate(text)
	if err != nil {
		return err
	}
	if err := s.activate(p.GUID, p.Account); err != nil {
		return err
	}
	s.opt.Logger.Info("account device selected", "account", p.Account, "computer_name", p.ComputerName, "new_device", created)
	return nil
}

func (s *Server) computerName(guid, account string) string {
	if s.opt.Store == nil {
		return ""
	}
	if account != "" {
		if p, err := s.opt.Store.Peek(account); err == nil && p.ComputerName != "" {
			return p.ComputerName
		}
	}
	raw, err := hex.DecodeString(guid)
	if err != nil || len(raw) != 16 {
		return ""
	}
	return device.ComputerName(raw, s.opt.Store.Platform())
}

func (s *Server) activate(guid, account string) error {
	raw, err := hex.DecodeString(guid)
	if err != nil || len(raw) != 16 {
		return fmt.Errorf("device GUID must contain 16 bytes")
	}
	number, _ := strconv.ParseUint(account, 10, 32)
	b, err := s.opt.Factory(raw, uint32(number))
	if err != nil {
		return err
	}
	s.mu.RLock()
	prior, priorPhase := s.active, s.state.Phase
	s.mu.RUnlock()
	if prior != nil && priorPhase == "online" {
		if err := pauseManaged(prior); err != nil {
			_ = b.Close()
			return err
		}
	}
	name := s.computerName(guid, account)
	s.mu.Lock()
	old, monitor := s.active, s.monitorCancel
	oldAPI := s.api
	s.monitorCancel = nil
	s.active = b
	s.api = api.NewServer(b, s.opt.Logger, s.opt.API)
	s.state.Account = account
	s.state.GUID = guid
	s.state.ComputerName = name
	s.state.Phase = "ready"
	s.state.Error = ""
	s.state.Notice = ""
	s.state.Online = false
	s.state.Nickname = ""
	s.state.QR = ""
	s.state.QRExpires = nil
	s.state.Messages = 0
	s.state.Notices = 0
	s.mu.Unlock()
	if monitor != nil {
		monitor()
	}
	if adapter, ok := oldAPI.(*api.Server); ok {
		_ = adapter.Close()
	}
	if old != nil {
		_ = old.Close()
	}
	return nil
}

func (s *Server) anonymous() error {
	s.mu.RLock()
	guid, account := s.state.GUID, s.state.Account
	s.mu.RUnlock()
	if guid == "" || account != "" {
		raw := make([]byte, 16)
		if _, err := rand.Read(raw); err != nil {
			return err
		}
		guid = hex.EncodeToString(raw)
	}
	return s.activate(guid, "")
}

func (s *Server) setPhase(phase string, err error) {
	s.mu.Lock()
	s.state.Phase = phase
	s.state.Error = ""
	if err != nil {
		s.state.Error = err.Error()
	}
	if phase == "online" || phase == "error" || phase == "offline" || phase == "cancelled" {
		s.state.QR = ""
		s.state.QRExpires = nil
	}
	s.mu.Unlock()
}

func (s *Server) prepare(ctx context.Context, account string) ([]byte, error) {
	s.mu.RLock()
	phase := s.state.Phase
	s.mu.RUnlock()
	if phase == "online" {
		return nil, fmt.Errorf("account is online; disconnect before requesting a new code")
	}
	if account != "" {
		if err := s.selectAccount(account); err != nil {
			return nil, err
		}
	}
	s.mu.RLock()
	b, phase := s.active, s.state.Phase
	s.mu.RUnlock()
	if b == nil {
		if err := s.anonymous(); err != nil {
			return nil, err
		}
		s.mu.RLock()
		b, phase = s.active, s.state.Phase
		s.mu.RUnlock()
	}
	// A failed or expired attempt needs a fresh connection, retaining the same device.
	if phase != "ready" {
		s.mu.RLock()
		a, guid := s.state.Account, s.state.GUID
		s.mu.RUnlock()
		if err := s.activate(guid, a); err != nil {
			return nil, err
		}
		s.mu.RLock()
		b = s.active
		s.mu.RUnlock()
	}
	s.setPhase("requesting", nil)
	s.opt.Logger.Info("requesting login QR code")
	image, err := b.QRCode(ctx)
	if err != nil {
		return nil, err
	}
	expires := time.Now().Add(120 * time.Second)
	s.mu.Lock()
	s.state.QR = "data:image/png;base64," + base64.StdEncoding.EncodeToString(image)
	s.state.QRExpires = &expires
	s.state.Phase = "waiting"
	s.mu.Unlock()
	if s.opt.QRFile != "" {
		if err := os.WriteFile(s.opt.QRFile, image, 0600); err != nil {
			return nil, fmt.Errorf("save QR image: %w", err)
		}
	}
	s.opt.Logger.Info("QR code ready; waiting for phone confirmation", "expires_in", 120)
	return image, nil
}

func (s *Server) complete(ctx context.Context) error {
	s.mu.RLock()
	b, a, guid, phase := s.active, s.state.Account, s.state.GUID, s.state.Phase
	s.mu.RUnlock()
	if b == nil || phase != "waiting" {
		return fmt.Errorf("request a QR code before logging in")
	}
	if progress, ok := b.(interface{ SetLoginProgress(func(string)) }); ok {
		progress.SetLoginProgress(func(next string) {
			if next != "authenticating" && next != "securing" && next != "registering" {
				return
			}
			s.mu.Lock()
			current := s.state.Phase
			if s.active != b || (current != "waiting" && current != "authenticating" && current != "securing" && current != "registering") {
				s.mu.Unlock()
				return
			}
			s.state.Phase = next
			s.state.QR = ""
			s.state.QRExpires = nil
			s.mu.Unlock()
			if current != next {
				s.opt.Logger.Info("login progress", "phase", next)
			}
		})
		defer progress.SetLoginProgress(nil)
	}
	if err := b.LoadSession(ctx); err != nil {
		return err
	}
	return s.acceptOnline(ctx, b, a, guid)
}

func (s *Server) acceptOnline(ctx context.Context, b ManagedBot, a, guid string) error {
	own, err := b.Self(ctx)
	if err != nil {
		return err
	}
	actual := strconv.FormatUint(uint64(own.Uin), 10)
	if own.Uin == 0 || (a != "" && actual != a) {
		_ = b.Close()
		return fmt.Errorf("signed-in account does not match selected account")
	}
	if !own.Online {
		return fmt.Errorf("server has not accepted the online registration")
	}
	s.mu.Lock()
	s.state.Notice = ""
	s.mu.Unlock()
	if a == "" {
		profile, _, err := s.opt.Store.BindLogin(actual, guid)
		if err != nil {
			s.opt.Logger.Warn("could not persist account device", "error", err)
			s.mu.Lock()
			s.state.Notice = "登录已完成，但设备档案保存失败；请查看日志。"
			s.mu.Unlock()
		} else if profile.GUID != guid {
			s.mu.Lock()
			s.state.Notice = "本次扫码使用了新设备；该账号原有设备档案已保留。下次请从保存账号中选择，复用原有设备。"
			s.mu.Unlock()
			s.opt.Logger.Warn("anonymous login used a new device; saved account device retained", "account", actual)
		}
		s.mu.Lock()
		s.state.Account = actual
		s.mu.Unlock()
		a = actual
	} else if err := s.opt.Store.MarkLogin(a); err != nil {
		s.opt.Logger.Warn("could not persist login timestamp", "error", err)
	}
	if saver, ok := b.(interface{ PersistLogin() error }); ok {
		if err := saver.PersistLogin(); err != nil {
			s.opt.Logger.Warn("QQ online but login tickets could not be saved", "error", err)
			s.mu.Lock()
			if s.state.Notice == "" {
				s.state.Notice = "账号已在线，但登录票据保存失败；重启后可能需要扫码，请查看日志。"
			}
			s.mu.Unlock()
		}
	}
	s.setPhase("online", nil)
	s.opt.Logger.Info("account is online", "account", a)
	s.monitor(b)
	return nil
}

func (s *Server) monitor(b ManagedBot) {
	source, ok := b.(interface {
		Subscribe() (<-chan protocol.MessageEvent, func())
	})
	if !ok {
		return
	}
	ctx, cancel := context.WithCancel(s.root)
	events, unsub := source.Subscribe()
	s.mu.Lock()
	s.monitorCancel = cancel
	s.mu.Unlock()
	go func() {
		defer unsub()
		for {
			select {
			case <-ctx.Done():
				return
			case e, ok := <-events:
				if !ok {
					return
				}
				s.mu.Lock()
				if e.Msg != nil {
					s.state.Messages++
				}
				if e.Notice != nil {
					s.state.Notices++
				}
				s.mu.Unlock()
				if e.Msg != nil {
					s.oopCommand(ctx, b, e)
					s.opt.Logger.Info("message received", "sender", e.Msg.SenderUin, "group", e.Msg.GroupUin)
				} else if e.Notice != nil {
					s.opt.Logger.Info("notice received")
				}
			}
		}
	}()
}

func (s *Server) loginError(err error) {
	s.mu.RLock()
	b, phase := s.active, s.state.Phase
	s.mu.RUnlock()
	if phase == "restoring" {
		s.mu.Lock()
		s.state.Notice = "登录恢复未完成。可手动重试；票据或认证失效时请重新扫码。"
		s.mu.Unlock()
	}
	if phase == "online" {
		s.opt.Logger.Warn("login request rejected", "error", err)
		return
	}
	if b != nil {
		if s.root.Err() != nil {
			suspendManaged(b)
		} else {
			if phase == "restoring" && errors.Is(err, context.Canceled) {
				_ = pauseManaged(b)
			}
			_ = b.Close()
		}
	}
	if s.opt.OnStopped != nil {
		s.opt.OnStopped()
	}
	if errors.Is(err, context.Canceled) {
		s.setPhase("cancelled", nil)
		s.opt.Logger.Info("login cancelled; device identity retained")
		return
	}
	s.setPhase("error", err)
	s.opt.Logger.Error("login failed", "error", err)
}

// Begin starts a login independently of the browser connection.
func (s *Server) Begin(account string) error {
	account = strings.TrimSpace(account)
	if account != "" {
		if _, err := device.Account(account); err != nil {
			return err
		}
	}
	if !s.op.TryLock() {
		return fmt.Errorf("a login is already running; cancel it first")
	}
	s.mu.RLock()
	online := s.state.Phase == "online"
	s.mu.RUnlock()
	if online {
		s.op.Unlock()
		return fmt.Errorf("account is online; disconnect before requesting a new code")
	}
	ctx, cancel := context.WithTimeout(s.root, 180*time.Second)
	s.mu.Lock()
	s.cancel = cancel
	s.state.QR = ""
	s.state.Error = ""
	s.mu.Unlock()
	go func() {
		defer s.op.Unlock()
		defer cancel()
		defer func() { s.mu.Lock(); s.cancel = nil; s.mu.Unlock() }()
		if account == "" {
			if err := s.anonymous(); err != nil {
				s.loginError(err)
				return
			}
		}
		if _, err := s.prepare(ctx, account); err != nil {
			s.loginError(err)
			return
		}
		if err := s.complete(ctx); err != nil {
			s.loginError(err)
		}
	}()
	return nil
}

func (s *Server) Cancel() {
	s.mu.RLock()
	cancel := s.cancel
	s.mu.RUnlock()
	if cancel != nil {
		cancel()
	}
}
func (s *Server) Disconnect() error {
	return s.disconnect(false)
}

func (s *Server) disconnect(forget bool) error {
	s.Cancel()
	if !s.op.TryLock() {
		return fmt.Errorf("cancelling login; retry disconnect after it stops")
	}
	defer s.op.Unlock()
	s.mu.Lock()
	b, monitor := s.active, s.monitorCancel
	s.monitorCancel = nil
	s.mu.Unlock()
	if monitor != nil {
		monitor()
	}
	var stopErr error
	if b != nil {
		if !forget {
			stopErr = pauseManaged(b)
		}
		stopErr = errors.Join(stopErr, b.Close())
		if forget {
			if clear, ok := b.(interface{ ForgetLogin() error }); ok {
				stopErr = errors.Join(stopErr, clear.ForgetLogin())
			}
		}
	}
	s.setPhase("offline", nil)
	if s.opt.OnStopped != nil {
		s.opt.OnStopped()
	}
	s.opt.Logger.Info("account disconnected; device identity retained")
	return stopErr
}
func (s *Server) Close() error {
	s.stop()
	s.Cancel()
	s.op.Lock()
	defer s.op.Unlock()
	s.mu.RLock()
	b, monitor := s.active, s.monitorCancel
	adapter, _ := s.api.(*api.Server)
	s.mu.RUnlock()
	if adapter != nil {
		_ = adapter.Close()
	}
	if monitor != nil {
		monitor()
	}
	if b != nil {
		return suspendManaged(b)
	}
	return nil
}

type accountView struct {
	device.Profile
	SavedLogin *bot.SavedLoginStatus `json:"saved_login,omitempty"`
}

func (s *Server) accountViews(profiles []device.Profile) []accountView {
	saved := map[string]loginstate.Metadata{}
	if s.opt.LoginStore != nil {
		if all, err := s.opt.LoginStore.List(); err == nil {
			for _, m := range all {
				saved[m.Account+"/"+m.GUID] = m
			}
		}
	}
	out := make([]accountView, 0, len(profiles))
	for _, p := range profiles {
		v := accountView{Profile: p}
		if m, ok := saved[p.Account+"/"+p.GUID]; ok {
			status := bot.SavedLoginStatus{Saved: true, Restorable: m.InvalidReason == "", AutoResume: m.AutoResume, Reason: m.InvalidReason}
			v.SavedLogin = &status
		}
		out = append(out, v)
	}
	return out
}

func send(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func failure(w http.ResponseWriter, status int, err error) {
	send(w, status, map[string]string{"error": err.Error()})
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !protectConsole(w, r) {
		return
	}
	if strings.HasPrefix(r.URL.Path, "/console/api/") {
		s.control(w, r)
		return
	}
	if (r.URL.Path == "/" || strings.HasPrefix(r.URL.Path, "/console/")) && !s.opt.Headless {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/console/", http.StatusTemporaryRedirect)
			return
		}
		http.StripPrefix("/console/", s.static).ServeHTTP(w, r)
		return
	}
	if r.URL.Path == "/v1/login/getqrcode" && r.Method == http.MethodGet {
		s.legacyQR(w, r)
		return
	}
	if (r.URL.Path == "/v1/login/loadsession" || r.URL.Path == "/v2/login/loadsession") && r.Method == http.MethodPost {
		s.legacyLoad(w, r)
		return
	}
	s.mu.RLock()
	handler := s.api
	s.mu.RUnlock()
	handler.ServeHTTP(w, r)
}

func (s *Server) control(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/console/api/")
	if r.Method == http.MethodGet {
		switch path {
		case "status":
			send(w, 200, s.Snapshot())
			return
		case "accounts":
			profiles, err := s.opt.Store.List()
			if err != nil {
				failure(w, 500, err)
			} else {
				send(w, 200, s.accountViews(profiles))
			}
			return
		case "logs":
			after, _ := strconv.ParseUint(r.URL.Query().Get("after"), 10, 64)
			send(w, 200, s.opt.Logs.Since(after))
			return
		case "signer":
			s.signer(w, r)
			return
		}
	}
	if r.Method == http.MethodPost {
		switch path {
		case "oop":
			s.oopControl(w, r)
			return
		case "login":
			var input struct {
				Account string `json:"account"`
			}
			r.Body = http.MaxBytesReader(w, r.Body, 4096)
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				failure(w, 400, err)
				return
			}
			if err := s.Begin(input.Account); err != nil {
				failure(w, 409, err)
			} else {
				send(w, 202, map[string]bool{"accepted": true})
			}
			return
		case "cancel":
			s.Cancel()
			send(w, 202, map[string]bool{"accepted": true})
			return
		case "resume":
			var v struct {
				Account string `json:"account"`
			}
			r.Body = http.MaxBytesReader(w, r.Body, 4096)
			if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
				failure(w, 400, err)
				return
			}
			if v.Account == "" {
				v.Account = s.Snapshot().Account
			}
			if err := s.Resume(v.Account); err != nil {
				failure(w, 409, err)
			} else {
				send(w, 202, map[string]bool{"accepted": true})
			}
			return
		case "forget-login":
			if err := s.ForgetLogin(); err != nil {
				failure(w, 409, err)
			} else {
				send(w, 200, map[string]bool{"ok": true})
			}
			return
		case "disconnect":
			if err := s.Disconnect(); err != nil {
				failure(w, 409, err)
			} else {
				send(w, 200, map[string]bool{"ok": true})
			}
			return
		}
	}
	failure(w, 404, fmt.Errorf("unknown control endpoint or method"))
}

func (s *Server) signer(w http.ResponseWriter, r *http.Request) {
	u, err := url.Parse(s.opt.Signer)
	if err != nil {
		failure(w, 500, err)
		return
	}
	u.Path = strings.TrimSuffix(strings.TrimRight(u.Path, "/"), "/sign") + "/health"
	u.RawPath = ""
	u.RawQuery = ""
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		failure(w, 500, err)
		return
	}
	start := time.Now()
	response, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		send(w, 200, map[string]any{"ready": false, "error": "签名服务连接失败，请检查地址、密钥和网络"})
		return
	}
	defer response.Body.Close()
	var body struct {
		Ready   bool   `json:"ready"`
		Adapter string `json:"adapter"`
	}
	err = json.NewDecoder(http.MaxBytesReader(w, response.Body, 4096)).Decode(&body)
	send(w, 200, map[string]any{"ready": response.StatusCode == 200 && err == nil && body.Ready, "adapter": body.Adapter, "latency_ms": time.Since(start).Milliseconds()})
}

func legacy(w http.ResponseWriter, value any, err error) {
	ret, msg := 0, ""
	if err != nil {
		ret = -1
		msg = err.Error()
	}
	send(w, 200, map[string]any{"CgiBaseResponse": map[string]any{"Ret": ret, "ErrMsg": msg}, "ResponseData": value, "Data": nil})
}
func (s *Server) legacyQR(w http.ResponseWriter, r *http.Request) {
	if !s.op.TryLock() {
		legacy(w, nil, fmt.Errorf("a login is already running"))
		return
	}
	defer s.op.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	image, err := s.prepare(ctx, r.URL.Query().Get("qq"))
	if err != nil {
		s.loginError(err)
		legacy(w, nil, err)
		return
	}
	legacy(w, map[string]string{"Qrcode": "data:image/png;base64," + base64.StdEncoding.EncodeToString(image)}, nil)
}
func (s *Server) legacyLoad(w http.ResponseWriter, r *http.Request) {
	if !s.op.TryLock() {
		legacy(w, nil, fmt.Errorf("a login is already running"))
		return
	}
	defer s.op.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), 180*time.Second)
	defer cancel()
	s.mu.Lock()
	s.cancel = cancel
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.cancel = nil; s.mu.Unlock() }()
	if err := s.complete(ctx); err != nil {
		s.loginError(err)
		legacy(w, nil, err)
		return
	}
	legacy(w, map[string]bool{"LoadSession": true}, nil)
}

func protectConsole(w http.ResponseWriter, r *http.Request) bool {
	// The management surface is local. Reject cross-origin requests, including
	// loopback DNS-rebinding attempts, before they can trigger a login or read logs.
	if strings.HasPrefix(r.URL.Path, "/console/") || r.URL.Path == "/" {
		hostname := r.Host
		if h, _, err := net.SplitHostPort(r.Host); err == nil {
			hostname = h
		}
		if hostname != "127.0.0.1" && hostname != "localhost" && hostname != "::1" {
			http.Error(w, "local console only", http.StatusForbidden)
			return false
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			u, err := url.Parse(origin)
			if err != nil || u.Host != r.Host {
				http.Error(w, "cross-origin request rejected", http.StatusForbidden)
				return false
			}
		}
		if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			http.Error(w, "cross-site console request rejected", http.StatusForbidden)
			return false
		}
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'")
	}
	return true
}
