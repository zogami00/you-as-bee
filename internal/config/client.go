package config

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// DefaultAPIPort is the management API port on the Pi agent.
const DefaultAPIPort = 3241

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

// ClientConfig is the configuration for the Windows client (yab).
type ClientConfig struct {
	SchemaVersion  int             `json:"schema_version"`
	Servers        []ServerConfig  `json:"servers"`
	UsbipPath      string          `json:"usbip_path"`
	AutoAttach     []AutoAttach    `json:"auto_attach"`
	Reconnect      ReconnectConfig `json:"reconnect"`
	CommandTimeout Duration        `json:"command_timeout"`
	LogFile        string          `json:"log_file"`
	LogLevel       string          `json:"log_level"`
}

func (c *ClientConfig) setDefaults() {
	c.SchemaVersion = 1
	c.Reconnect.Initial = Duration(1 * time.Second)
	c.Reconnect.Max = Duration(30 * time.Second)
	c.CommandTimeout = Duration(15 * time.Second)
	c.LogLevel = "info"
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
	if !validLogLevels[c.LogLevel] {
		return fmt.Errorf("log_level: unknown level %q", c.LogLevel)
	}

	return nil
}
