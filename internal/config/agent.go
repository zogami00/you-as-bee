package config

import (
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"
)

// Device operating modes.
const (
	// ModeAlways exports the device as soon as it is present.
	ModeAlways = "always"
	// ModeOnDemand exports the device only when a client asks for it.
	ModeOnDemand = "on_demand"
)

// Recognised log levels and formats.
var (
	validLogLevels  = map[string]bool{"debug": true, "info": true, "warn": true, "error": true}
	validLogFormats = map[string]bool{"text": true, "json": true}
)

var (
	deviceNameRe = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)
	hexQuadRe    = regexp.MustCompile(`^[0-9a-fA-F]{4}$`)
)

// USBIPConfig locates the Linux USB/IP tooling. The usbipd daemon is run by
// systemd, not by the agent, so there is no manage flag here.
type USBIPConfig struct {
	Bin       string `json:"bin"`
	UsbipdBin string `json:"usbipd_bin"`
}

// DeviceConfig describes a single USB device the agent should manage.
type DeviceConfig struct {
	// Name is the stable, unique, lower-case identifier (^[a-z0-9-]{1,32}$).
	Name string `json:"name"`
	// VID and PID are 4-hex-digit identifiers (case-normalised to lower case).
	VID string `json:"vid"`
	PID string `json:"pid"`
	// Serial and Port are optional narrowing selectors. When both are empty and
	// a VID/PID matches several physical devices, the match is ambiguous.
	Serial string `json:"serial,omitempty"`
	Port   string `json:"port,omitempty"`
	// Mode is ModeAlways or ModeOnDemand (default ModeOnDemand).
	Mode string `json:"mode,omitempty"`
}

// AgentConfig is the configuration for the Raspberry Pi agent (yabd).
type AgentConfig struct {
	SchemaVersion  int            `json:"schema_version"`
	Listen         string         `json:"listen"`
	TokenFile      string         `json:"token_file"`
	AllowedClients []string       `json:"allowed_clients"`
	PollInterval   Duration       `json:"poll_interval"`
	USBIP          USBIPConfig    `json:"usbip"`
	Devices        []DeviceConfig `json:"devices"`
	LogLevel       string         `json:"log_level"`
	LogFormat      string         `json:"log_format"`
}

// DefaultAllowedClients is the private-address CIDR allowlist used when none is
// configured.
var DefaultAllowedClients = []string{
	"192.168.0.0/16",
	"10.0.0.0/8",
	"172.16.0.0/12",
}

// DefaultListen is the management API bind address (USB/IP itself is always on
// 3240 and is deliberately not configurable).
const DefaultListen = "0.0.0.0:3241"

func (c *AgentConfig) setDefaults() {
	c.SchemaVersion = 1
	c.Listen = DefaultListen
	c.TokenFile = "/etc/you-as-bee/token"
	c.AllowedClients = append([]string(nil), DefaultAllowedClients...)
	c.PollInterval = Duration(5 * time.Second)
	c.USBIP.Bin = "/usr/sbin/usbip"
	c.USBIP.UsbipdBin = "/usr/sbin/usbipd"
	c.LogLevel = "info"
	c.LogFormat = "text"
}

// Validate checks the agent configuration and normalises VID/PID case and
// default device modes in place.
func (c *AgentConfig) Validate() error {
	if c.SchemaVersion != 1 {
		return fmt.Errorf("schema_version: want 1, got %d", c.SchemaVersion)
	}
	if strings.TrimSpace(c.Listen) == "" {
		return fmt.Errorf("listen: must not be empty")
	}
	if strings.TrimSpace(c.TokenFile) == "" {
		return fmt.Errorf("token_file: must not be empty")
	}
	for _, cidr := range c.AllowedClients {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			return fmt.Errorf("allowed_clients: invalid CIDR %q", cidr)
		}
	}
	if c.PollInterval <= 0 {
		return fmt.Errorf("poll_interval: must be positive")
	}
	if strings.TrimSpace(c.USBIP.Bin) == "" {
		return fmt.Errorf("usbip.bin: must not be empty")
	}
	if strings.TrimSpace(c.USBIP.UsbipdBin) == "" {
		return fmt.Errorf("usbip.usbipd_bin: must not be empty")
	}
	if !validLogLevels[c.LogLevel] {
		return fmt.Errorf("log_level: unknown level %q", c.LogLevel)
	}
	if !validLogFormats[c.LogFormat] {
		return fmt.Errorf("log_format: unknown format %q", c.LogFormat)
	}

	seen := make(map[string]bool, len(c.Devices))
	for i := range c.Devices {
		d := &c.Devices[i]
		if !deviceNameRe.MatchString(d.Name) {
			return fmt.Errorf("devices[%d].name: %q must match ^[a-z0-9-]{1,32}$", i, d.Name)
		}
		if seen[d.Name] {
			return fmt.Errorf("devices[%d].name: duplicate device name %q", i, d.Name)
		}
		seen[d.Name] = true

		if !hexQuadRe.MatchString(d.VID) {
			return fmt.Errorf("devices[%d].vid: %q is not a 4-hex-digit id", i, d.VID)
		}
		if !hexQuadRe.MatchString(d.PID) {
			return fmt.Errorf("devices[%d].pid: %q is not a 4-hex-digit id", i, d.PID)
		}
		d.VID = strings.ToLower(d.VID)
		d.PID = strings.ToLower(d.PID)

		if d.Mode == "" {
			d.Mode = ModeOnDemand
		}
		switch d.Mode {
		case ModeAlways, ModeOnDemand:
		default:
			return fmt.Errorf("devices[%d].mode: unknown mode %q", i, d.Mode)
		}
	}

	return nil
}
