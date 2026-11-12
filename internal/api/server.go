package api

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"openopq/internal/bot"
)

// Options control which protocol adapters the server exposes.
type Options struct {
	// ClusterInfo reports all accounts when the API belongs to a multi-account hub.
	ClusterInfo func(context.Context) (any, error)
	// OPQ enables the OPQ-compatible ws (/ws) and leaves LuaApiCaller/login as they are.
	OPQ bool
	// OneBot enables the OneBot v11 ws (/onebot/v11/ws) and HTTP actions (/onebot/v11/...).
	OneBot12UserID string
	OneBot12       bool
	OneBot12Peers  func(context.Context) []*Server
	OneBot         bool
	OneBotConfig   OneBotConfig
}

// Server routes the framework's HTTP API.
type Server struct {
	bot    bot.Bot
	mux    *http.ServeMux
	logger *slog.Logger
	opts   Options
	ob     *oneBotRuntime
	v12    *oneBot12Runtime
}

// NewServer wires the routes onto a bot implementation. A zero Options enables the OPQ adapter,
// which is the historical default.
func NewServer(implementation bot.Bot, logger *slog.Logger, opts Options) *Server {
	if !opts.OPQ && !opts.OneBot && !opts.OneBot12 {
		opts.OPQ = true
	}
	if logger == nil {
		logger = slog.Default()
	}
	server := &Server{bot: implementation, mux: http.NewServeMux(), logger: logger, opts: opts}
	server.ob = newOneBotRuntime(server)
	if opts.OneBot12 {
		server.v12 = newOneBot12Runtime(server)
	}
	_ = server.ConfigureOneBot(opts.OneBotConfig)
	server.routes()
	return server
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.opts.OneBot12 && IsOneBot12Path(r.URL.Path) {
		s.serve12(w, r)
		return
	}
	if s.opts.OneBot && IsOneBotPath(r.URL.Path) {
		if IsOneBotWSPath(r.URL.Path) && strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			s.handleOneBotWS(w, r)
		} else {
			s.handleOneBotHTTP(w, r)
		}
		return
	}
	s.mux.ServeHTTP(w, r)
}

func (s *Server) Close() error {
	if s.v12 != nil {
		s.v12.close()
	}
	s.ob.close()
	return nil
}

func (s *Server) routes() {
	// Login + the generic caller are always available (sending works under either protocol).
	s.mux.HandleFunc("/v1/login/getqrcode", s.handleQRCode)
	s.mux.HandleFunc("/v1/login/loadsession", s.handleLoadSession)
	s.mux.HandleFunc("/v2/login/loadsession", s.handleLoadSession)
	s.mux.HandleFunc("/v1/clusterinfo", s.handleClusterInfo)
	s.mux.HandleFunc("/v1/LuaApiCaller", s.handleLuaAPICaller)
	if s.opts.OPQ {
		// OPQBot-compatible event push: a websocket streaming incoming messages as OPQ events.
		s.mux.HandleFunc("/ws", s.handleWS)
	}
	if s.opts.OneBot {
		// OneBot v11: forward websocket + HTTP actions.
		s.mux.HandleFunc("/onebot/v11/ws", s.handleOneBotWS)
		s.mux.HandleFunc("/onebot/v11/", s.handleOneBotHTTP)
	}
	s.mux.HandleFunc("/", s.handleUnknown)
}

// handleUnknown answers the way the original does for a path it does not serve, so a
// client can tell "wrong route" from "wrong request".
func (s *Server) handleUnknown(w http.ResponseWriter, r *http.Request) {
	s.logger.Debug("unrouted request", "method", r.Method, "path", r.URL.Path)
	writeBase(w, RetError, "not found")
}
