package config

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DefaultAPIPort is the management API port on the Pi agent.
const DefaultAPIPort = 3241

// usbip-win2 receive modes. low-latency avoids the zero-copy default, which
// floods Windows with device-change events against some devices (measured on
// real hardware: 86 events in 45s under zero-copy vs 5 in 60s under
// low-latency, with matching USB stall/reset messages in the Pi kernel log).
const (
	// ReceiveModeLowLatency is the default: usbip-win2 --receive-mode low-latency.
	ReceiveModeLowLatency = "low-latency"
	// ReceiveModeZeroCopy is usbip-win2's own default.
	ReceiveModeZeroCopy = "zero-copy"
)

// validReceiveModes are the receive_mode values the client accepts.
var validReceiveModes = map[string]bool{
	ReceiveModeLowLatency: true,
	ReceiveModeZeroCopy:   true,
}

var tokenRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ServerConfig describes one Pi agent the Windows client can talk to.
type ServerConfig struct {
	// Name is the unique, human-facing server identifier.
	Name string `json:"name"`
	// Host is the DNS name or IP address of the agent.
	Host string `json:"host"`
	// APIPort is the agent management API port (default 3241).
	APIPort int `json:"api_port"`
	// Token is the 64-lower-case-hex bearer token.
	Token string `json:"token"`
}

// AutoAttach asks the client to attach Device from Server at startup.
type AutoAttach struct {
	Server string `json:"server"`
	Device string `json:"device"`
}

// ReconnectConfig controls exponential backoff while reconnecting.
type ReconnectConfig struct {
	Initial Duration `json:"initial"`
	Max     Duration `json:"max"`
}

// DefaultWebUIListen is the default bind address of the Windows local web UI.
// Port 0 asks the OS for a free port.
const DefaultWebUIListen = "127.0.0.1:0"

// WebUIConfig configures the Windows local browser UI served by `yab tray`.
type WebUIConfig struct {
	// Enabled serves the local UI. It is on by default; set it to false to
	// disable the local server entirely.
	Enabled bool `json:"enabled"`
	// Listen is the loopback bind address. It must be a loopback IP (for
	// example 127.0.0.1:0, where 0 asks the OS for a free port). A wildcard
	// (0.0.0.0), a LAN address or a host name is rejected.
	Listen string `json:"listen"`
}

// ClientConfig is the configuration for the Windows client (yab).
type ClientConfig struct {
	SchemaVersion  int             `json:"schema_version"`
	Servers        []ServerConfig  `json:"servers"`
	UsbipPath      string          `json:"usbip_path"`
	AutoAttach     []AutoAttach    `json:"auto_attach"`
	Reconnect      ReconnectConfig `json:"reconnect"`
	CommandTimeout Duration        `json:"command_timeout"`
	// ReceiveMode selects the usbip-win2 attach receive mode: low-latency
	// (default) or zero-copy. See the ReceiveMode* constants.
	ReceiveMode string `json:"receive_mode"`
	LogFile     string `json:"log_file"`
	LogLevel    string `json:"log_level"`
	// WebUI configures the local Windows browser UI. It is enabled by default.
	WebUI WebUIConfig `json:"web_ui"`
}

func (c *ClientConfig) setDefaults() {
	c.SchemaVersion = 1
	c.Reconnect.Initial = Duration(1 * time.Second)
	c.Reconnect.Max = Duration(30 * time.Second)
	c.CommandTimeout = Duration(15 * time.Second)
	c.ReceiveMode = ReceiveModeLowLatency
	c.LogLevel = "info"
	// The local UI is on by default; an explicit "web_ui": {"enabled": false}
	// still wins because defaults are applied before the document is decoded.
	c.WebUI.Enabled = true
	c.WebUI.Listen = DefaultWebUIListen
}

// Validate checks the client configuration and fills per-server defaults.
func (c *ClientConfig) Validate() error {
	if c.SchemaVersion != 1 {
		return fmt.Errorf("schema_version: want 1, got %d", c.SchemaVersion)
	}

	serverNames := make(map[string]bool, len(c.Servers))
	serverHosts := make(map[string]bool, len(c.Servers))
	for i := range c.Servers {
		s := &c.Servers[i]
		if strings.TrimSpace(s.Name) == "" {
			return fmt.Errorf("servers[%d].name: must not be empty", i)
		}
		// A server name becomes the left half of every qualified pin id
		// ("server/device"), so it must not contain "/" (or be empty): a name
		// such as "a/b" would make "a/b/dev" parse as server "a", device
		// "b/dev" and never resolve. Constrain it exactly like a device name.
		if !deviceNameRe.MatchString(s.Name) {
			return fmt.Errorf("servers[%d].name: %q must match ^[a-z0-9-]{1,32}$", i, s.Name)
		}
		if serverNames[s.Name] {
			return fmt.Errorf("servers[%d].name: duplicate server name %q", i, s.Name)
		}
		serverNames[s.Name] = true

		if strings.TrimSpace(s.Host) == "" {
			return fmt.Errorf("servers[%d].host: must not be empty", i)
		}
		if serverHosts[s.Host] {
			return fmt.Errorf("servers[%d].host: duplicate server host %q", i, s.Host)
		}
		serverHosts[s.Host] = true

		if s.APIPort == 0 {
			s.APIPort = DefaultAPIPort
		}
		if s.APIPort < 1 || s.APIPort > 65535 {
			return fmt.Errorf("servers[%d].api_port: %d out of range 1-65535", i, s.APIPort)
		}
		if !tokenRe.MatchString(s.Token) {
			return fmt.Errorf("servers[%d].token: must be exactly 64 lower-case hex characters", i)
		}
	}

	seenPairs := make(map[string]bool, len(c.AutoAttach))
	for i, a := range c.AutoAttach {
		if !serverNames[a.Server] {
			return fmt.Errorf("auto_attach[%d].server: unknown server %q", i, a.Server)
		}
		if strings.TrimSpace(a.Device) == "" {
			return fmt.Errorf("auto_attach[%d].device: must not be empty", i)
		}
		key := a.Server + "\x00" + a.Device
		if seenPairs[key] {
			return fmt.Errorf("auto_attach[%d]: duplicate pair (%q, %q)", i, a.Server, a.Device)
		}
		seenPairs[key] = true
	}

	if c.Reconnect.Max < c.Reconnect.Initial {
		return fmt.Errorf("reconnect.max: %s must not be less than reconnect.initial %s",
			c.Reconnect.Max, c.Reconnect.Initial)
	}
	if c.CommandTimeout <= 0 {
		return fmt.Errorf("command_timeout: must be positive")
	}
	if c.ReceiveMode == "" {
		c.ReceiveMode = ReceiveModeLowLatency
	}
	if !validReceiveModes[c.ReceiveMode] {
		return fmt.Errorf("receive_mode: unknown mode %q (want %q or %q)",
			c.ReceiveMode, ReceiveModeLowLatency, ReceiveModeZeroCopy)
	}
	if !validLogLevels[c.LogLevel] {
		return fmt.Errorf("log_level: unknown level %q", c.LogLevel)
	}

	if strings.TrimSpace(c.WebUI.Listen) == "" {
		c.WebUI.Listen = DefaultWebUIListen
	}
	if err := validateWebUIListen(c.WebUI.Listen); err != nil {
		return err
	}

	return nil
}

// validateWebUIListen requires the local UI to bind a loopback IP. A wildcard,
// a LAN address or a host name is rejected here as well as at the server, so a
// misconfiguration cannot expose the UI beyond the machine.
func validateWebUIListen(listen string) error {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return fmt.Errorf("web_ui.listen: %q must be host:port: %w", listen, err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("web_ui.listen: %q must bind a loopback IP such as %s", listen, DefaultWebUIListen)
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 0 || p > 65535 {
		return fmt.Errorf("web_ui.listen: %q has an invalid port", listen)
	}
	return nil
}
