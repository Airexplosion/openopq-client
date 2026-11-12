// Command openopq is an open implementation of the OPQBot framework API.
//
// It serves the endpoints existing OPQ clients call and leaves the client protocol
// behind a single interface, so the protocol side can be swapped without touching the
// HTTP surface.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"openopq/internal/api"
	"openopq/internal/buildinfo"
	"openopq/internal/config"
	"openopq/internal/console"
	"openopq/internal/device"
	"openopq/internal/loginstate"
)

func main() {
	if err := run(); err != nil {
		slog.Error("openopq stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		openBrowser = flag.Bool("open-browser", len(os.Args) == 1, "open the local console after listening (default with no arguments)")
		port        = flag.Int("port", 8087, "HTTP API port")
		configPath  = flag.String("config", "Config.json", "framework configuration file")
		verbose     = flag.Bool("verbose", false, "log every request")
		dataDir     = flag.String("data-dir", "data", "account device profiles and runtime files")
		account     = flag.String("account", "", "QQ account to select (each account keeps its own device GUID)")
		headless    = flag.Bool("headless", false, "disable the webpage; keep HTTP/WS and control APIs")
		quiet       = flag.Bool("quiet", false, "suppress terminal logs; retain console/file logs")
		logFile     = flag.String("log-file", "", "append JSON logs to this file")
		autoLogin   = flag.Bool("login", false, "start QR login for --account on startup")
		resumeLogin = flag.Bool("resume", true, "restore saved QQ logins on startup (false skips automatic restore)")
		qrFile      = flag.String("qr-file", "", "write the current QR PNG to this path")
		showVersion = flag.Bool("version", false, "print application version and exit")
	)
	var attachConsoles []string
	flag.Func("attach-console", "attach a loopback console or /accounts/<session>; repeat for multiple running accounts", func(value string) error {
		attachConsoles = append(attachConsoles, value)
		return nil
	})
	flag.Parse()
	if len(os.Args) == 1 {
		if exe, e := os.Executable(); e == nil {
			if _, e = os.Stat(filepath.Join(filepath.Dir(exe), *configPath)); e == nil {
				if e = os.Chdir(filepath.Dir(exe)); e != nil {
					return e
				}
			}
		}
	}
	if *showVersion {
		fmt.Printf("openopq %s (%s), built %s\n", buildinfo.Version, buildinfo.Commit, buildinfo.BuiltAt)
		return nil
	}
	if *port < 1 || *port > 65535 {
		return fmt.Errorf("port must be in 1..65535")
	}
	if *account != "" {
		if _, err := device.Account(*account); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(*dataDir, 0700); err != nil {
		return err
	}

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	logs := console.NewLogBuffer(level)
	handlers := []slog.Handler{logs}
	var terminal io.Writer = os.Stdout
	if *quiet {
		terminal = io.Discard
	}
	handlers = append(handlers, slog.NewTextHandler(terminal, &slog.HandlerOptions{Level: level}))
	if *logFile != "" {
		file, err := os.OpenFile(*logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		defer file.Close()
		handlers = append(handlers, slog.NewJSONHandler(file, &slog.HandlerOptions{Level: level}))
	}
	logger := slog.New(console.MultiHandler(handlers...))
	slog.SetDefault(logger)

	loaded, err := config.Load(*configPath)
	if err != nil {
		return fmt.Errorf("%w; put Config.json beside the executable or pass --config (signer must be installed separately)", err)
	}
	opq, onebot := loaded.Protocols()
	platform := device.DetectPlatform(loaded.Version)
	store, err := device.NewWithPlatform(filepath.Join(*dataDir, "devices"), platform)
	if err != nil {
		return err
	}
	var loginStore *loginstate.Store
	if loaded.PersistLogin == nil || *loaded.PersistLogin {
		loginStore, err = loginstate.New(filepath.Join(*dataDir, "qq-logins"))
		if err != nil {
			return err
		}
		meta, listErr := loginStore.List()
		auto := 0
		for _, m := range meta {
			if m.AutoResume && m.InvalidReason == "" {
				auto++
			}
		}
		if listErr != nil {
			logger.Warn("could not read some saved QQ logins", "error", listErr, "saved", len(meta), "auto_resume", auto)
		} else {
			logger.Info("saved QQ logins loaded", "saved", len(meta), "auto_resume", auto)
		}
	}
	if *qrFile == "" {
		*qrFile = filepath.Join(*dataDir, "login-qr.png")
	}
	security, err := console.LoadSecurity(*dataDir)
	if err != nil {
		return fmt.Errorf("load console security: %w", err)
	}
	if _, err := os.Stat(filepath.Join(*dataDir, "console-key.txt")); err == nil {
		logger.Info("console key authentication enabled", "bootstrap_key_file", absolutePath(filepath.Join(*dataDir, "console-key.txt")))
	} else {
		logger.Info("console key authentication enabled; use the configured console key")
	}
	app := console.NewHub(console.Options{Store: store, Logger: logger, Logs: logs, API: api.Options{OPQ: opq, OneBot: onebot, OneBot12: loaded.OneBot12Enabled(), OneBotConfig: loaded.OneBot}, Version: loaded.Version, Signer: loaded.SignEndpoint(), Headless: *headless, QRFile: *qrFile,
		TransportDir: filepath.Join(*dataDir, "transports"),
		SharedPort:   *port,
		Security:     security,
		LoginStore:   loginStore,
		SessionFactory: func(guid []byte, uin uint32, sessionLogger *slog.Logger) (console.ManagedBot, error) {
			b, err := buildBotWithDevice(loaded, sessionLogger, guid, uin, computerNameFor(store, guid, uin), filepath.Join(*dataDir, "signer-sessions"))
			if err == nil {
				b.SetLoginStore(loginStore)
			}
			return b, err
		},
	})
	defer app.Close()
	for _, endpoint := range attachConsoles {
		if err := app.Attach(endpoint); err != nil {
			return err
		}
	}
	for _, account := range accountsBesides(loaded.Accounts, *account) {
		if _, err := app.Add(account); err != nil {
			return err
		}
	}
	logger.Info("configuration loaded", "version", loaded.Version, "protocol", loaded.Protocol, "headless", *headless)
	if os.Getenv("OPENOPQ_GUID") != "" || os.Getenv("OPENOPQ_GUID_FILE") != "" {
		logger.Warn("legacy GUID overrides ignored; each QQ account uses its saved device profile")
	}
	if *account != "" {
		if err := app.Select(*account); err != nil {
			return err
		}
	}
	if *autoLogin {
		if *account == "" && len(loaded.Accounts) > 0 {
			for _, a := range loaded.Accounts {
				if err := app.BeginAccount(a); err != nil {
					return err
				}
			}
		} else {
			if err := app.Begin(*account); err != nil {
				return err
			}
		}
	}
	server := &http.Server{
		Addr:              fmt.Sprintf("127.0.0.1:%d", *port),
		Handler:           app,
		ReadHeaderTimeout: 10 * time.Second,
	}

	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	consoleURL := fmt.Sprintf("http://127.0.0.1:%d/console/", *port)
	if !*autoLogin && *resumeLogin && (loaded.AutoRestoreLogin == nil || *loaded.AutoRestoreLogin) {
		if err := app.ResumeSavedLogins(*account); err != nil {
			logger.Warn("some saved QQ logins could not be scheduled for restore", "error", err)
		}
	}
	logger.Info("console ready; signer must already be available", "url", consoleURL, "data_dir", absolutePath(*dataDir))
	if *openBrowser && !*headless {
		openLocalBrowser(consoleURL, logger)
	}
	return serve(server, logger, listener)
}

// serve runs until an interrupt arrives or the listener fails.
func serve(server *http.Server, logger *slog.Logger, listener net.Listener) error {
	idle := make(chan struct{})
	go func() {
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
		<-stop
		logger.Info("shutting down")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			logger.Error("shutdown failed", "error", err)
		}
		close(idle)
	}()

	logger.Info("openopq listening", "address", server.Addr)
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("listen: %w", err)
	}
	<-idle
	return nil
}

// accountsBesides drops the --account QQ from the configured account list.
// --account is selected in the default session, so adding it again would give
// the same QQ two sessions; OneBot then cannot tell which one a connection
// means and rejects every client with "unknown/ambiguous selector".
func accountsBesides(accounts []string, selected string) []string {
	selected, _ = device.Account(selected)
	rest := make([]string, 0, len(accounts))
	for _, a := range accounts {
		if normalized, err := device.Account(a); selected != "" && err == nil && normalized == selected {
			continue
		}
		rest = append(rest, a)
	}
	return rest
}

func absolutePath(path string) string {
	p, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return p
}
func openLocalBrowser(url string, logger *slog.Logger) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		logger.Warn("open browser manually", "url", url)
		return
	}
	go func() { _ = cmd.Wait() }()
}
