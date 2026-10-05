//go:build windows

// Command yab is the Windows USB-over-LAN client. It drives usbip-win2 to make
// controllers exported by a Raspberry Pi appear as real local USB devices, and
// it keeps them attached with a supervisor plus a system-tray UI.
package main

import (
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"

	"github.com/zogami00/you-as-bee/internal/client"
	"github.com/zogami00/you-as-bee/internal/config"
	"github.com/zogami00/you-as-bee/internal/elevate"
	"github.com/zogami00/you-as-bee/internal/execx"
	"github.com/zogami00/you-as-bee/internal/usbipwin"
	"github.com/zogami00/you-as-bee/internal/version"
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
		return config.ClientConfig{}, err
	}
	return cfg, nil
}

// locateTool finds usbip.exe for the config, or returns a typed error.
func locateTool(cfg config.ClientConfig) (*usbipwin.Tool, error) {
	path, err := usbipwin.Locate(cfg.UsbipPath)
	if err != nil {
		return nil, err
	}
	return usbipwin.New(path, execx.New()), nil
}

// newManager builds the supervisor. When tool is nil a no-op USBIP is used so
// commands such as `status` still work without usbip.exe installed.
func newManager(cfg config.ClientConfig, tool *usbipwin.Tool) (*client.Manager, error) {
	var u client.USBIP = noopUSBIP{}
	if tool != nil {
		u = tool
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return client.New(client.Options{
		Config: cfg,
		USBIP:  u,
		Log:    logger,
		Notify: func(pin, msg string) { logger.Warn("notify", "pin", pin, "message", msg) },
	})
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

func autoAttachFor(cfg config.ClientConfig, device string) (config.AutoAttach, bool) {
	for _, a := range cfg.AutoAttach {
		if a.Device == device {
			return a, true
		}
	}
	return config.AutoAttach{}, false
}
