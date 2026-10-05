package main

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/zogami00/you-as-bee/internal/config"
	"github.com/zogami00/you-as-bee/internal/identity"
	"github.com/zogami00/you-as-bee/internal/sysfs"
)

var busIDRe = regexp.MustCompile(`^[0-9]+-[0-9]+(\.[0-9]+)*$`)

// selector is a pin name, a "vid:pid[:serial]" triple, or a USB bus id.
type selector struct {
	raw string
}

func parseSelector(s string) (selector, error) {
	if strings.TrimSpace(s) == "" {
		return selector{}, fmt.Errorf("empty selector")
	}
	return selector{raw: s}, nil
}

// matches reports whether dev is selected. pinName is the configured pin name
// for dev, when known.
func (s selector) matches(pinName string, dev sysfs.Device) bool {
	if pinName != "" && s.raw == pinName {
		return true
	}
	if busIDRe.MatchString(s.raw) {
		return s.raw == dev.BusID
	}
	if strings.Contains(s.raw, ":") {
		parts := strings.Split(s.raw, ":")
		if len(parts) < 2 || len(parts) > 3 {
			return false
		}
		if !strings.EqualFold(parts[0], dev.VID) || !strings.EqualFold(parts[1], dev.PID) {
			return false
		}
		if len(parts) == 3 && parts[2] != dev.Serial {
			return false
		}
		return true
	}
	return false
}

// resolveDevice finds the single attached device a selector identifies.
func resolveDevice(fsys sysfs.FS, root string, pins []config.DeviceConfig, sel selector) (sysfs.Device, config.DeviceConfig, error) {
	devs, err := sysfs.Enumerate(fsys, root)
	if err != nil {
		return sysfs.Device{}, config.DeviceConfig{}, err
	}

	// A pin name selects the pin's device.
	for _, pin := range pins {
		if sel.raw != pin.Name {
			continue
		}
		for _, d := range devs {
			if identity.Match(pin, d) {
				return d, pin, nil
			}
		}
		return sysfs.Device{}, pin, fmt.Errorf("pin %q is not attached", pin.Name)
	}

	var matches []sysfs.Device
	for _, d := range devs {
		if sel.matches("", d) {
			matches = append(matches, d)
		}
	}
	switch len(matches) {
	case 0:
		return sysfs.Device{}, config.DeviceConfig{}, fmt.Errorf("no device matches %q", sel.raw)
	case 1:
		return matches[0], pinFor(matches[0], pins), nil
	default:
		return sysfs.Device{}, config.DeviceConfig{}, fmt.Errorf("selector %q matches %d devices", sel.raw, len(matches))
	}
}

func pinFor(dev sysfs.Device, pins []config.DeviceConfig) config.DeviceConfig {
	for _, p := range pins {
		if identity.Match(p, dev) {
			return p
		}
	}
	return config.DeviceConfig{}
}
