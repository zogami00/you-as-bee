package sysfs

import (
	"path"
	"strconv"
	"strings"
)

// Iface is one USB interface function of a device, as exposed by a
// "<busid>:1.N" sysfs directory.
type Iface struct {
	// BusID is the interface bus id, e.g. "1-1.4:1.0".
	BusID string
	// Driver is the kernel driver bound to the interface (empty when none).
	Driver string
}

// Device is a USB device as described by sysfs.
type Device struct {
	// BusID is the stable USB bus id, e.g. "1-1.4".
	BusID string
	// VID and PID are lower-case 4-hex-digit identifiers.
	VID string
	PID string
	// Serial is the device serial string; empty when the device has none.
	Serial string
	// Manufacturer and Product are human-readable strings.
	Manufacturer string
	Product      string
	// Driver is the kernel driver currently bound to the device, empty when
	// the device is unbound.
	Driver string
	// BusNum and DevNum are the bus and device numbers.
	BusNum int
	DevNum int
	// Speed is the sysfs speed string, e.g. "480".
	Speed string
	// Status is the usbip status attribute: 0 unbound, 1 exported, 2 attached,
	// 3 error.
	Status int
	// Interfaces lists the USB interface functions of the device.
	Interfaces []Iface
}

// Enumerate walks root (normally /sys/bus/usb/devices) and returns the USB
// devices attached to the bus.
//
// Entries that are not devices (no idVendor file, interface directories, root
// hubs named usbN, or hubs with bDeviceClass 09) are skipped. Optional
// attributes that are missing or unreadable leave the corresponding field at
// its zero value and are not an error.
func Enumerate(fsys FS, root string) ([]Device, error) {
	entries, err := fsys.ReadDir(root)
	if err != nil {
		return nil, err
	}

	var out []Device
	for _, e := range entries {
		if !e.Dir || strings.Contains(e.Name, ":") || isRootHub(e.Name) {
			continue
		}

		base := path.Join(root, e.Name)
		rawVID, err := fsys.ReadFile(path.Join(base, "idVendor"))
		if err != nil || strings.TrimSpace(string(rawVID)) == "" {
			continue
		}
		// Hubs (bDeviceClass 09) are not exportable devices.
		if attr(fsys, path.Join(base, "bDeviceClass")) == "09" {
			continue
		}

		d := Device{
			BusID:        e.Name,
			VID:          strings.ToLower(strings.TrimSpace(string(rawVID))),
			PID:          strings.ToLower(attr(fsys, path.Join(base, "idProduct"))),
			Serial:       attr(fsys, path.Join(base, "serial")),
			Manufacturer: attr(fsys, path.Join(base, "manufacturer")),
			Product:      attr(fsys, path.Join(base, "product")),
			Speed:        attr(fsys, path.Join(base, "speed")),
			BusNum:       atoi(attr(fsys, path.Join(base, "busnum"))),
			DevNum:       atoi(attr(fsys, path.Join(base, "devnum"))),
			Status:       atoi(attr(fsys, path.Join(base, "usbip_status"))),
		}
		d.Driver = linkBase(fsys, path.Join(base, "driver"))
		d.Interfaces = interfaces(fsys, base)
		out = append(out, d)
	}
	return out, nil
}

func interfaces(fsys FS, base string) []Iface {
	entries, err := fsys.ReadDir(base)
	if err != nil {
		return nil
	}
	var out []Iface
	for _, e := range entries {
		if !strings.Contains(e.Name, ":") {
			continue
		}
		out = append(out, Iface{
			BusID:  e.Name,
			Driver: linkBase(fsys, path.Join(base, e.Name, "driver")),
		})
	}
	return out
}

// attr reads a sysfs attribute and trims surrounding whitespace. A missing or
// unreadable attribute yields the empty string and is never an error.
func attr(fsys FS, name string) string {
	b, err := fsys.ReadFile(name)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// linkBase returns the base name of the target of a symlink, or "" when the
// link is missing or unreadable.
func linkBase(fsys FS, name string) string {
	target, err := fsys.ReadLink(name)
	if err != nil || target == "" {
		return ""
	}
	return path.Base(target)
}

func atoi(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}

// isRootHub reports whether name is a root hub directory such as "usb1".
func isRootHub(name string) bool {
	if !strings.HasPrefix(name, "usb") {
		return false
	}
	rest := name[len("usb"):]
	if rest == "" {
		return false
	}
	for _, r := range rest {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
