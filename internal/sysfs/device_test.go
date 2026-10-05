package sysfs

import (
	"os"
	"path/filepath"
	"testing"
)

func buildTree() *FakeFS {
	const root = "/sys/bus/usb/devices"
	f := NewFakeFS()

	// Root hub: must be skipped.
	f.AddFile(root+"/usb1/idVendor", "1d6b")
	f.AddFile(root+"/usb1/idProduct", "0002")
	f.AddFile(root+"/usb1/bDeviceClass", "09")

	// External hub: has idVendor but bDeviceClass 09 => skipped.
	f.AddFile(root+"/1-1/idVendor", "05e3")
	f.AddFile(root+"/1-1/idProduct", "0608")
	f.AddFile(root+"/1-1/bDeviceClass", "09")

	// Bluetooth dongle with NO serial attribute.
	f.AddFile(root+"/1-1.2/idVendor", "0A12")
	f.AddFile(root+"/1-1.2/idProduct", "0001")
	f.AddFile(root+"/1-1.2/manufacturer", "Cambridge Silicon Radio, Ltd")
	f.AddFile(root+"/1-1.2/product", "Bluetooth Dongle (HCI mode)")
	f.AddFile(root+"/1-1.2/busnum", "1")
	f.AddFile(root+"/1-1.2/devnum", "5")
	f.AddFile(root+"/1-1.2/speed", "12")
	// On a real Pi the device-level driver is "usb"; btusb binds to the
	// interfaces.
	f.AddLink(root+"/1-1.2/driver", "../../../../bus/usb/drivers/usb")
	f.AddLink(root+"/1-1.2/1-1.2:1.0/driver", "../../../../../bus/usb/drivers/btusb")
	// Second interface deliberately has no driver link.
	f.AddDir(root + "/1-1.2/1-1.2:1.1")

	// Xbox wireless adapter with a serial.
	f.AddFile(root+"/1-1.4/idVendor", "045e")
	f.AddFile(root+"/1-1.4/idProduct", "02e6")
	f.AddFile(root+"/1-1.4/serial", "XBOX-0001")
	f.AddFile(root+"/1-1.4/manufacturer", "Microsoft")
	f.AddFile(root+"/1-1.4/product", "Xbox Wireless Adapter")
	f.AddFile(root+"/1-1.4/busnum", "1")
	f.AddFile(root+"/1-1.4/devnum", "7")
	f.AddFile(root+"/1-1.4/speed", "480")
	f.AddFile(root+"/1-1.4/usbip_status", "1")
	f.AddLink(root+"/1-1.4/driver", "../../../../bus/usb/drivers/usbip-host")

	// Interface directory at the top level must be ignored.
	f.AddFile(root+"/1-1.4:1.0/idVendor", "045e")

	// An entry without idVendor must be ignored.
	f.AddFile(root+"/1-2/uevent", "MAJOR=189")

	return f
}

func TestEnumerateExcludesHubsAndNonDevices(t *testing.T) {
	devs, err := Enumerate(buildTree(), "/sys/bus/usb/devices")
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}
	if len(devs) != 2 {
		names := make([]string, 0, len(devs))
		for _, d := range devs {
			names = append(names, d.BusID)
		}
		t.Fatalf("got %d devices %v, want 2 (1-1.2, 1-1.4)", len(devs), names)
	}
}

func TestEnumerateMissingSerialTolerated(t *testing.T) {
	devs, err := Enumerate(buildTree(), "/sys/bus/usb/devices")
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}
	bt := find(t, devs, "1-1.2")
	if bt.Serial != "" {
		t.Errorf("Serial = %q, want empty", bt.Serial)
	}
	if bt.VID != "0a12" || bt.PID != "0001" {
		t.Errorf("VID/PID = %q/%q, want 0a12/0001 (lower-cased)", bt.VID, bt.PID)
	}
	if bt.BusNum != 1 || bt.DevNum != 5 {
		t.Errorf("BusNum/DevNum = %d/%d, want 1/5", bt.BusNum, bt.DevNum)
	}
	if bt.Speed != "12" {
		t.Errorf("Speed = %q, want 12", bt.Speed)
	}
	if bt.Driver != "usb" {
		t.Errorf("Driver = %q, want usb (device-level driver on a real Pi)", bt.Driver)
	}
	if bt.Product != "Bluetooth Dongle (HCI mode)" {
		t.Errorf("Product = %q", bt.Product)
	}
}

func TestEnumerateInterfacesAndStatus(t *testing.T) {
	devs, err := Enumerate(buildTree(), "/sys/bus/usb/devices")
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}

	bt := find(t, devs, "1-1.2")
	if len(bt.Interfaces) != 2 {
		t.Fatalf("Interfaces = %+v, want 2", bt.Interfaces)
	}
	if bt.Interfaces[0].BusID != "1-1.2:1.0" || bt.Interfaces[0].Driver != "btusb" {
		t.Errorf("interface 0 = %+v", bt.Interfaces[0])
	}
	if bt.Interfaces[1].BusID != "1-1.2:1.1" || bt.Interfaces[1].Driver != "" {
		t.Errorf("interface 1 = %+v, want no driver", bt.Interfaces[1])
	}

	xbox := find(t, devs, "1-1.4")
	if xbox.Serial != "XBOX-0001" {
		t.Errorf("Serial = %q", xbox.Serial)
	}
	if xbox.Driver != "usbip-host" {
		t.Errorf("Driver = %q, want usbip-host", xbox.Driver)
	}
	if xbox.Status != 1 {
		t.Errorf("Status = %d, want 1", xbox.Status)
	}
}

func TestEnumerateMissingRootIsError(t *testing.T) {
	if _, err := Enumerate(NewFakeFS(), "/sys/bus/usb/devices"); err == nil {
		t.Fatal("expected error for missing root, got nil")
	}
}

// TestEnumerateSymlinkedDeviceDirectory reproduces the real sysfs layout: each
// entry in /sys/bus/usb/devices is a symlink to the device directory. A
// symlinked directory must be enumerated like a real one.
func TestEnumerateSymlinkedDeviceDirectory(t *testing.T) {
	const root = "/sys/bus/usb/devices"
	target := "/sys/devices/platform/soc/usb1/1-1/1-1.2"

	f := NewFakeFS()
	f.AddLinkDir(root+"/1-1.2", target)
	f.AddFile(target+"/idVendor", "0A12")
	f.AddFile(target+"/idProduct", "0001")
	f.AddFile(target+"/manufacturer", "Cambridge Silicon Radio, Ltd")
	f.AddFile(target+"/product", "Bluetooth Dongle (HCI mode)")
	f.AddFile(target+"/devnum", "5")
	f.AddFile(target+"/usbip_status", "0")
	f.AddLink(target+"/driver", "/sys/bus/usb/drivers/usb")
	f.AddLinkDir(root+"/1-1.2/1-1.2:1.0", target+"/1-1.2:1.0")
	f.AddFile(target+"/1-1.2:1.0/idVendor", "0A12")
	f.AddLink(target+"/1-1.2:1.0/driver", "/sys/bus/usb/drivers/btusb")

	devs, err := Enumerate(f, root)
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}
	if len(devs) != 1 {
		t.Fatalf("got %d devices, want 1 (the symlinked 1-1.2)", len(devs))
	}
	d := devs[0]
	if d.BusID != "1-1.2" || d.VID != "0a12" || d.PID != "0001" {
		t.Errorf("device = %+v", d)
	}
	if d.Driver != "usb" {
		t.Errorf("Driver = %q, want usb", d.Driver)
	}
	if len(d.Interfaces) != 1 || d.Interfaces[0].Driver != "btusb" {
		t.Errorf("Interfaces = %+v, want one with driver btusb", d.Interfaces)
	}
}

// TestOSFSReadDirResolvesDirectorySymlink guards the production OSFS, which is
// the code that actually sees sysfs. It skips when the platform cannot create
// symlinks (for example an unprivileged Windows session).
func TestOSFSReadDirResolvesDirectorySymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(dir, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(filepath.Join(dir, "missing"), filepath.Join(dir, "broken")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	ents, err := OSFS{}.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	got := make(map[string]bool, len(ents))
	for _, e := range ents {
		got[e.Name] = e.Dir
	}
	if !got["real"] {
		t.Error("real directory reported as non-directory")
	}
	if !got["link"] {
		t.Error("symlink to a directory must be reported as a directory")
	}
	if got["broken"] {
		t.Error("broken symlink must degrade to a non-directory")
	}
}

func find(t *testing.T, devs []Device, busid string) Device {
	t.Helper()
	for _, d := range devs {
		if d.BusID == busid {
			return d
		}
	}
	t.Fatalf("device %q not found among %d devices", busid, len(devs))
	return Device{}
}
