//go:build windows

// Command yab is the Windows USB-over-LAN client. It drives usbip-win2 to make
// controllers exported by a Raspberry Pi appear as real local USB devices, and
// it keeps them attached with a supervisor plus a system-tray UI.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/zogami00/you-as-bee/internal/client"
	"github.com/zogami00/you-as-bee/internal/config"
	"github.com/zogami00/you-as-bee/internal/elevate"
	"github.com/zogami00/you-as-bee/internal/execx"
	"github.com/zogami00/you-as-bee/internal/usbipwin"
	"github.com/zogami00/you-as-bee/internal/version"
	"github.com/zogami00/you-as-bee/internal/webui"
)

func main() {
	os.Exit(dispatch(os.Args[1:], os.Stdout, os.Stderr))
}

func dispatch(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return cmdTray(nil, stdout, stderr)
	}
	switch args[0] {
	case "tray":
		return cmdTray(args[1:], stdout, stderr)
	case "list", "devices":
		return cmdList(args[1:], stdout, stderr)
	case "attach":
		return cmdAttach(args[1:], stdout, stderr)
	case "detach":
		return cmdDetach(args[1:], stdout, stderr)
	case "status":
		return cmdStatus(args[1:], stdout, stderr)
	case "doctor":
		return cmdDoctor(args[1:], stdout, stderr)
	case "install":
		return cmdInstall(args[1:], stdout, stderr)
	case "uninstall":
		return cmdUninstall(args[1:], stdout, stderr)
	case "version":
		fmt.Fprintf(stdout, "yab %s\n", version.String())
		return 0
	case "-h", "--help", "help":
		printUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "yab: unknown command %q\n\n", args[0])
		printUsage(stderr)
		return 2
	}
}

func printUsage(w io.Writer) {
	fmt.Fprint(w, `yab - USB-over-LAN Windows client

Usage:
  yab tray                       [--config PATH]   (default with no arguments)
  yab list | devices             [--config PATH] [--json]
  yab attach <device>            [--config PATH]
  yab attach --all               [--config PATH]
  yab detach <device>            [--config PATH]
  yab detach --all               [--config PATH]
  yab status                     [--config PATH] [--json]
  yab doctor                     [--config PATH] [--json]
  yab install                    [--config PATH]
  yab uninstall                  [--config PATH]
  yab version

The default config path is %ProgramData%\you-as-bee\client.json.
`)
}

// programData returns %ProgramData% with a safe fallback.
func programData() string {
	if pd := os.Getenv("ProgramData"); pd != "" {
		return pd
	}
	return `C:\ProgramData`
}

// defaultConfigPath is the location of the client config.
func defaultConfigPath() string {
	return filepath.Join(programData(), "you-as-bee", "client.json")
}

func newFlagSet(name string) *flag.FlagSet {
	return flag.NewFlagSet(name, flag.ContinueOnError)
}

func loadConfig(path string) (config.ClientConfig, error) {
	var cfg config.ClientConfig
	if err := config.Load(path, &cfg); err != nil {
		return config.ClientConfig{}, configReadError(path, err)
	}
	return cfg, nil
}

// configReadError makes a permission failure actionable. %ProgramData%\you-as-bee
// is deliberately restricted to Administrators and SYSTEM because client.json
// holds the bearer token, so an unelevated read fails with a raw "Access is
// denied"; tell the user how to fix it instead.
func configReadError(path string, err error) error {
	if errors.Is(err, fs.ErrPermission) {
		return fmt.Errorf("cannot read %s; run from an elevated prompt (the config holds the API token)", path)
	}
	return err
}

// locateTool finds usbip.exe for the config, or returns a typed error.
func locateTool(cfg config.ClientConfig) (*usbipwin.Tool, error) {
	path, err := usbipwin.Locate(cfg.UsbipPath)
	if err != nil {
		return nil, err
	}
	tool := usbipwin.New(path, execx.New())
	tool.ReceiveMode = cfg.ReceiveMode
	return tool, nil
}

// newManager builds the supervisor. When tool is nil a no-op USBIP is used so
// commands such as `status` still work without usbip.exe installed.
func newManager(cfg config.ClientConfig, tool *usbipwin.Tool) (*client.Manager, error) {
	logger, _, err := newLogger(cfg)
	if err != nil {
		return nil, err
	}
	return newManagerWith(cfg, tool, logger)
}

// newManagerWith builds the supervisor with a caller-supplied logger, so the
// tray can share one logger (and its log ring) with the local web UI.
func newManagerWith(cfg config.ClientConfig, tool *usbipwin.Tool, logger *slog.Logger) (*client.Manager, error) {
	var u client.USBIP = noopUSBIP{}
	if tool != nil {
		u = tool
	}
	return client.New(client.Options{
		Config: cfg,
		USBIP:  u,
		Log:    logger,
		Notify: func(pin, msg string) { logger.Warn("notify", "pin", pin, "message", msg) },
	})
}

// newLogger builds the client logger and returns the in-memory ring the local
// web UI serves at /ui/api/logs. log_file empty means records still go to the
// ring and are otherwise discarded; otherwise the file is opened in append mode
// so a long-running tray keeps its history. log_level selects the minimum
// level.
func newLogger(cfg config.ClientConfig) (*slog.Logger, *webui.LogRing, error) {
	level := slog.LevelInfo
	switch strings.ToLower(strings.TrimSpace(cfg.LogLevel)) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	opts := &slog.HandlerOptions{Level: level}
	ring := webui.NewLogRing()

	output := io.Writer(io.Discard)
	if strings.TrimSpace(cfg.LogFile) != "" {
		f, err := os.OpenFile(cfg.LogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return nil, nil, fmt.Errorf("open log_file: %w", err)
		}
		output = f
	}
	// Fan out to the console/file handler and the ring so GET /ui/api/logs sees
	// exactly what the client logs.
	return slog.New(slog.NewMultiHandler(slog.NewTextHandler(output, opts), ring)), ring, nil
}

// requireElevated prints the required message and returns false when the
// process is not elevated.
func requireElevated(stderr io.Writer) bool {
	if elevate.IsElevated() {
		return true
	}
	fmt.Fprintln(stderr, "yab: administrator rights required; run from an elevated prompt or run 'yab install'")
	return false
}

func apiBase(cfg config.ClientConfig, s config.ServerConfig) string {
	return "http://" + net.JoinHostPort(s.Host, strconv.Itoa(s.APIPort))
}

func serverByHost(cfg config.ClientConfig, name string) (config.ServerConfig, bool) {
	for _, s := range cfg.Servers {
		if s.Name == name {
			return s, true
		}
	}
	return config.ServerConfig{}, false
}

// autoAttachFor resolves a configured auto_attach entry from either a
// qualified "server/device" id or a bare device name. A bare name that matches
// more than one server is ambiguous and reported as not found, so the caller
// tells the user to qualify it.
func autoAttachFor(cfg config.ClientConfig, device string) (config.AutoAttach, bool) {
	server, name := "", device
	if i := strings.Index(device, "/"); i >= 0 {
		server, name = device[:i], device[i+1:]
	}

	matches := make([]config.AutoAttach, 0, 1)
	for _, a := range cfg.AutoAttach {
		if a.Device != name {
			continue
		}
		if server != "" && a.Server != server {
			continue
		}
		matches = append(matches, a)
	}
	if len(matches) == 1 {
		return matches[0], true
	}
	return config.AutoAttach{}, false
}
