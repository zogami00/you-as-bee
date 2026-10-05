//go:build windows

package usbipwin

import (
	"os"
	"path/filepath"
	"testing"
)

func withElevated(t *testing.T, v bool) {
	t.Helper()
	old := elevated
	elevated = func() bool { return v }
	t.Cleanup(func() { elevated = old })
}

func userPath(t *testing.T) string {
	t.Helper()
	root := os.Getenv("USERPROFILE")
	if root == "" {
		t.Skip("USERPROFILE not set")
	}
	return filepath.Join(root, "AppData", "Local", "Programs", "USBip", "usbip.exe")
}

func TestCheckUserWritableRefusesDiscoveredPathWhenElevated(t *testing.T) {
	withElevated(t, true)
	p := userPath(t)
	if root := userWritableRoot(p); root == "" {
		t.Fatalf("expected %q to be user-writable", p)
	}
	if err := checkUserWritable(p, false); err == nil {
		t.Fatal("elevated + discovered user-writable path must be refused")
	}
}

func TestCheckUserWritableAllowsExplicitOptIn(t *testing.T) {
	withElevated(t, true)
	p := userPath(t)
	if err := checkUserWritable(p, true); err != nil {
		t.Fatalf("explicit usbip_path is the operator's opt-in, got %v", err)
	}
}

func TestCheckUserWritableAllowsDiscoveredPathWhenNotElevated(t *testing.T) {
	withElevated(t, false)
	p := userPath(t)
	if err := checkUserWritable(p, false); err != nil {
		t.Fatalf("without elevation there is no privilege to escalate, got %v", err)
	}
}
