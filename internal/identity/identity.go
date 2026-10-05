// Package identity derives a stable key for a USB device and resolves the
// agent's configured pins against the devices that are actually attached.
package identity

import (
	"strings"

	"github.com/zogami00/you-as-bee/internal/config"
	"github.com/zogami00/you-as-bee/internal/sysfs"
)

// Key returns the stable identity of a device.
//
// It is "vid:pid:serial" when the device reports a serial number, and
// "vid:pid@busid" otherwise. VID and PID are lower-cased so that the key does
// not depend on the case sysfs happened to report.
func Key(d sysfs.Device) string {
	vid := strings.ToLower(d.VID)
	pid := strings.ToLower(d.PID)
	if d.Serial != "" {
		return vid + ":" + pid + ":" + d.Serial
	}
	return vid + ":" + pid + "@" + d.BusID
}

// Match reports whether a pin selects a device.
//
// A pin always matches on VID and PID (case-insensitively). When the pin sets
// Serial, the device serial must match exactly. When it sets Port, the device
// bus id must match exactly.
func Match(p config.DeviceConfig, d sysfs.Device) bool {
	if !equalHex(p.VID, d.VID) || !equalHex(p.PID, d.PID) {
		return false
	}
	if p.Serial != "" && p.Serial != d.Serial {
		return false
	}
	if p.Port != "" && p.Port != d.BusID {
		return false
	}
	return true
}

// Ambiguity reports a pin that matched more than one attached device.
type Ambiguity struct {
	// Pin is the configured pin name.
	Pin string
	// Devices are the present devices that matched it.
	Devices []sysfs.Device
}

// Resolve maps pin names to the single attached device each one selects.
//
// A pin that matches no attached device is simply absent from the result. A pin
// that matches more than one device is ambiguous: it is reported separately and
// never guessed. Every device passed in is considered attached.
func Resolve(pins []config.DeviceConfig, devs []sysfs.Device) (map[string]sysfs.Device, []Ambiguity, error) {
	resolved := make(map[string]sysfs.Device)
	var ambiguities []Ambiguity

	for _, p := range pins {
		var matches []sysfs.Device
		for _, d := range devs {
			if Match(p, d) {
				matches = append(matches, d)
			}
		}
		switch len(matches) {
		case 0:
			// No match: the pin is absent, which is not an error.
		case 1:
			resolved[p.Name] = matches[0]
		default:
			ambiguities = append(ambiguities, Ambiguity{Pin: p.Name, Devices: matches})
		}
	}

	return resolved, ambiguities, nil
}

func equalHex(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}
