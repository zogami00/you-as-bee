//go:build windows

package execx

import "testing"

// TestChildSysProcAttrHidesWindow asserts the Windows attribute that stops a
// windowless tray from flashing a console for every usbip.exe call.
func TestChildSysProcAttrHidesWindow(t *testing.T) {
	attr := childSysProcAttr()
	if attr == nil {
		t.Fatal("childSysProcAttr() = nil on Windows, want a SysProcAttr")
	}
	if !attr.HideWindow {
		t.Error("HideWindow = false, want true")
	}
	const createNoWindow = 0x08000000
	if attr.CreationFlags&createNoWindow == 0 {
		t.Errorf("CreationFlags = %#x, want CREATE_NO_WINDOW (%#x) set", attr.CreationFlags, createNoWindow)
	}
	// Optional sanity: the full flag set is what we intend, not extra bits.
	if attr.CreationFlags != createNoWindow {
		t.Errorf("CreationFlags = %#x, want exactly %#x", attr.CreationFlags, createNoWindow)
	}
}
