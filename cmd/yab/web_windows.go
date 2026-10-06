//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/zogami00/you-as-bee/internal/client"
	"github.com/zogami00/you-as-bee/internal/config"
	"github.com/zogami00/you-as-bee/internal/webui"
)

// webBackend adapts the tray controller and the supervisor to webui.Backend.
// Attach and Detach call the same methods the tray menu uses (Detach maps to
// Pause and does not unexport on the Pi); Status and Servers read the manager.
type webBackend struct {
	tc *trayController
	m  *client.Manager
}

// newWebBackend builds the adapter. tc may be nil in tests that only read
// state; Attach/Detach then report that the tray is unavailable.
func newWebBackend(tc *trayController, m *client.Manager) webBackend {
	return webBackend{tc: tc, m: m}
}

func (b webBackend) Status() []webui.PinStatus {
	statuses := b.m.Status()
	out := make([]webui.PinStatus, 0, len(statuses))
	for _, ps := range statuses {
		out = append(out, webui.PinStatus{
			Pin:         ps.Pin,
			Server:      ps.Server,
			State:       ps.State,
			BusID:       ps.BusID,
			Port:        ps.Port,
			Paused:      ps.Paused,
			PauseReason: ps.PauseReason,
			LastError:   ps.LastError,
		})
	}
	return out
}

func (b webBackend) Servers() []webui.ServerStatus {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	statuses := b.m.CheckServers(ctx)
	out := make([]webui.ServerStatus, 0, len(statuses))
	for _, s := range statuses {
		out = append(out, webui.ServerStatus{
			Name:        s.Name,
			Host:        s.Host,
			APIPort:     s.APIPort,
			Reachable:   s.Reachable,
			TokenValid:  s.TokenValid,
			DeviceCount: s.DeviceCount,
			Err:         s.Err,
		})
	}
	return out
}

func (b webBackend) Attach(pin string) error {
	if b.tc == nil {
		return errors.New("tray is not available")
	}
	return b.tc.Attach(pin)
}

func (b webBackend) Detach(pin string) error {
	if b.tc == nil {
		return errors.New("tray is not available")
	}
	return b.tc.Detach(pin)
}

// startLocalWebUI binds the loopback listener, records the actual port (which
// matters when Listen asked for port 0) and serves in the background.
func startLocalWebUI(cfg config.ClientConfig, backend webui.Backend, logs *webui.LogRing, logger *slog.Logger) (*webui.LocalServer, error) {
	srv, err := webui.NewLocal(webui.LocalConfig{
		Backend:    backend,
		Logs:       logs,
		Listen:     cfg.WebUI.Listen,
		UnknownErr: client.ErrUnknownPin,
		Log:        logger,
	})
	if err != nil {
		return nil, err
	}
	ln, err := srv.Listen()
	if err != nil {
		return nil, err
	}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Warn("web UI server stopped", "err", err)
		}
	}()
	logger.Info("web UI listening", "url", srv.BaseURL())
	return srv, nil
}

// OpenUI opens the local UI in the user's default browser. The tray runs
// elevated, so the URL is handed to explorer.exe rather than ShellExecute: the
// shell launches the browser de-elevated, and the browser never inherits the
// elevated token.
func (t *trayController) OpenUI() error {
	if t.web == nil {
		return errors.New("web UI is not running (check web_ui.enabled in client.json)")
	}
	rawURL := t.web.NewLoginURL()
	if err := launchBrowser(rawURL); err != nil {
		// Fall back to making the base URL available rather than failing the
		// tray: log it and put it in the error, but never the live one-time
		// code. The code is a credential for the 60s login window, and both
		// the log ring (GET /ui/api/logs) and the tray status title are
		// readable by anything that can see them. The base URL is enough for
		// the user to reopen the UI from the tray.
		base := t.web.BaseURL()
		if t.log != nil {
			t.log.Warn("could not open a browser; open the web UI again from the tray", "url", base, "err", err)
		}
		return fmt.Errorf("could not open a browser at %s; open the web UI again from the tray: %w", base, err)
	}
	return nil
}

// launchBrowser opens rawURL. It is a package variable so tests can force a
// launch failure without starting a browser.
var launchBrowser = openUI

// explorerPath returns the absolute path of explorer.exe under %SystemRoot%.
// An elevated process must not resolve explorer.exe through PATH (which can
// include a writable directory), mirroring the absolute pnputil.exe path in
// internal/usbipwin.
func explorerPath() string {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return filepath.Join(root, "explorer.exe")
}

// explorerCommand builds the explorer.exe hand-off for rawURL. It is a package
// variable so tests can substitute a launcher without invoking the real shell.
var explorerCommand = func(rawURL string) *exec.Cmd {
	return exec.Command(explorerPath(), rawURL)
}

// openUI opens rawURL in the default browser, de-elevated.
//
// It must start explorer.exe, not ShellExecute or rundll32: the tray runs
// elevated, and explorer.exe hands the URL to the already-running unelevated
// shell, so the browser does not inherit the elevated token.
//
// The URL must contain no query string. explorer.exe treats a URL containing
// "?" as a filesystem path: it launches no browser and opens a folder window.
// That is why NewLoginURL puts the one-time code in a path segment
// (GET /ui/login/<code>) instead of ?code=...; do not "tidy" it back into a
// query string.
//
// explorer.exe's exit status is NOT a failure signal: it exits 1 even when it
// successfully hands the URL to the shell (verified on Windows 11). There is
// deliberately no fallback launcher: a second launch would open the UI twice,
// and a fallback such as rundll32 would run from the elevated tray and could
// start the browser with the elevated token - exactly what the explorer
// hand-off avoids. Only a cmd.Start() failure is reported, so the tray's
// message can still tell the user to reopen the UI from the tray.
func openUI(rawURL string) error {
	cmd := explorerCommand(rawURL)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("explorer.exe: %w", err)
	}
	// Do not wait for explorer.exe; its exit status is meaningless and the
	// shell hand-off is already under way. Release the process handle.
	return cmd.Process.Release()
}
