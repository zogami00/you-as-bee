package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/zogami00/you-as-bee/internal/agent"
	"github.com/zogami00/you-as-bee/internal/api"
	"github.com/zogami00/you-as-bee/internal/config"
	"github.com/zogami00/you-as-bee/internal/sdnotify"
	"github.com/zogami00/you-as-bee/internal/sysfs"
	"github.com/zogami00/you-as-bee/internal/usbiphost"
	"github.com/zogami00/you-as-bee/internal/version"
)

// sysfsSource adapts sysfs enumeration to agent.Source.
type sysfsSource struct {
	fsys sysfs.FS
	root string
}

// Enumerate implements agent.Source.
func (s *sysfsSource) Enumerate() ([]sysfs.Device, error) {
	return sysfs.Enumerate(s.fsys, s.root)
}

func cmdRun(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("run")
	configPath := fs.String("config", defaultConfigPath, "path to the agent config file")
	listen := fs.String("listen", "", "override the management API listen address")
	logLevel := fs.String("log-level", "", "override the log level")
	logFormat := fs.String("log-format", "", "override the log format (text or json)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "yabd run: unexpected arguments")
		return 2
	}
	if err := runAgent(*configPath, *listen, *logLevel, *logFormat, stderr); err != nil {
		fmt.Fprintf(stderr, "yabd run: %v\n", err)
		return 1
	}
	return 0
}

func runAgent(configPath, listen, level, format string, output io.Writer) error {
	var cfg config.AgentConfig
	if err := config.Load(configPath, &cfg); err != nil {
		return err
	}
	if listen != "" {
		cfg.Listen = listen
	}
	if level != "" {
		cfg.LogLevel = level
	}
	if format != "" {
		cfg.LogFormat = format
	}

	logger := newLogger(cfg.LogLevel, cfg.LogFormat, output)

	token, err := readToken(cfg.TokenFile)
	if err != nil {
		return fmt.Errorf("token: %w", err)
	}

	hostname, _ := os.Hostname()

	binder := usbiphost.New()
	binder.Log = logger
	rec := agent.New(cfg.Devices, &sysfsSource{fsys: sysfs.OSFS{}, root: "/sys/bus/usb/devices"}, binder)
	rec.Log = logger
	rec.Version = version.Version
	rec.Hostname = hostname
	rec.Started = time.Now()
	rec.Interval = cfg.PollInterval.Duration()
	rec.UsbipdUp = func() bool { return portListening("127.0.0.1:3240") }

	srv, err := api.New(api.Config{
		Listen:         cfg.Listen,
		Token:          token,
		AllowedClients: cfg.AllowedClients,
		Backend:        rec,
		UnknownErr:     agent.ErrUnknown,
		Log:            logger,
	})
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	recDone := make(chan struct{})
	go func() {
		defer close(recDone)
		rec.Run(ctx)
	}()
	go watchdog(ctx)

	_ = sdnotify.Ready()
	_ = sdnotify.Status("serving on " + cfg.Listen)
	logger.Info("yabd started", "listen", cfg.Listen, "pins", len(cfg.Devices))

	var runErr error
	select {
	case <-ctx.Done():
		// Shutting down must never unbind devices: a stopped agent must not
		// drop clients that are already attached. But an in-flight bind or
		// unbind sequence must be allowed to finish, or a device could be left
		// driverless between two writes.
		logger.Info("shutting down; waiting for the current reconcile pass")
	case err := <-errCh:
		stop()
		runErr = fmt.Errorf("api server: %w", err)
	}

	if !waitReconciler(recDone, reconcileDrainTimeout) {
		logger.Warn("reconciler did not finish in time", "timeout", reconcileDrainTimeout)
	}
	if runErr != nil {
		return runErr
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Warn("api shutdown", "err", err)
	}
	return nil
}

// reconcileDrainTimeout bounds how long shutdown waits for the reconcile loop
// to finish the pass it is in.
const reconcileDrainTimeout = 30 * time.Second

// waitReconciler waits for the reconcile goroutine to exit, reporting false on
// timeout.
func waitReconciler(done <-chan struct{}, timeout time.Duration) bool {
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// watchdog resets the systemd watchdog timer until ctx is cancelled.
func watchdog(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = sdnotify.Watchdog()
		}
	}
}

// tokenHexRe is the same shape the client requires: 64 lower-case hex chars.
var tokenHexRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

func readToken(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return "", fmt.Errorf("%s is empty", path)
	}
	if !tokenHexRe.MatchString(token) {
		return "", fmt.Errorf("%s must contain exactly 64 lower-case hex characters, got %d characters", path, len(token))
	}
	return token, nil
}

func newLogger(level, format string, output io.Writer) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: lvl}
	var handler slog.Handler
	if format == "json" {
		handler = slog.NewJSONHandler(output, opts)
	} else {
		handler = slog.NewTextHandler(output, opts)
	}
	return slog.New(handler)
}

func portListening(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
