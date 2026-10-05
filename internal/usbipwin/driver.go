package usbipwin

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/zogami00/you-as-bee/internal/execx"
)

// Driver errors carry the exact user-facing guidance the CLI and doctor print.
var (
	// ErrDriverMissing means the usbip-win2 kernel driver (vhci) is not
	// installed or not reachable.
	ErrDriverMissing = errors.New("usbip-win2 driver not installed - reinstall usbip-win2")
	// ErrDriverBlocked means the driver is present but Windows blocked it,
	// typically with Code 52 (unsigned driver).
	ErrDriverBlocked = errors.New("usbip-win2 driver blocked (code 52) - install a signed release, or enable test signing per usbip-win2 docs")
)

var (
	// driverMissingRe must not match an ordinary per-device failure such as
	// "device not found"; it matches only text that points at the vhci driver
	// or service itself.
	driverMissingRe = regexp.MustCompile(`(?i)vhci|cannot open|(?:driver|service)\b[^\n]{0,40}\bnot\s+(?:found|installed|present|running)`)
	problem52Re     = regexp.MustCompile(`(?i)problem(?:\s+code)?\s*:?\s*52\b`)
	usbipDeviceRe   = regexp.MustCompile(`(?i)usbip|vhci`)
)

// ClassifyFailure maps a non-zero usbip.exe exit to a typed driver error when
// the stderr points at the vhci driver. A zero code returns nil.
func ClassifyFailure(ctx context.Context, r execx.Runner, exe string, code int, stderr string) error {
	if code == 0 {
		return nil
	}
	if driverMissingRe.MatchString(stderr) {
		if unsignedDriverProblem(ctx, r, exe) {
			return ErrDriverBlocked
		}
		return ErrDriverMissing
	}
	return fmt.Errorf("usbipwin: usbip exited %d: %s", code, strings.TrimSpace(stderr))
}

// unsignedDriverProblem consults `pnputil /enum-devices /problem` for a USB/IP
// device whose problem code is 52. Any failure to run or parse pnputil is
// reported as false so the caller falls back to the missing-driver guidance.
func unsignedDriverProblem(ctx context.Context, r execx.Runner, exe string) bool {
	if r == nil {
		return false
	}
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	pnputil := filepath.Join(root, "System32", "pnputil.exe")
	out, _, err := r.Run(ctx, pnputil, "/enum-devices", "/problem")
	if err != nil && out == "" {
		return false
	}
	return hasUSBIPProblem52(out)
}

// hasUSBIPProblem52 reports whether any blank-line-separated pnputil device
// block both names a usbip/vhci device and carries problem code 52.
func hasUSBIPProblem52(out string) bool {
	for _, block := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n\n") {
		if block == "" {
			continue
		}
		if usbipDeviceRe.MatchString(block) && problem52Re.MatchString(block) {
			return true
		}
	}
	return false
}

// DriverStatus reports whether a usbip/vhci device is present and whether it is
// blocked with problem code 52. It only observes; it never changes driver
// state. This is the read-only view `yab doctor` reports.
func DriverStatus(ctx context.Context, r execx.Runner, exe string) (present, unsigned bool) {
	if r == nil {
		return false, false
	}
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	pnputil := filepath.Join(root, "System32", "pnputil.exe")

	if out, _, err := r.Run(ctx, pnputil, "/enum-devices", "/problem"); err == nil || out != "" {
		if hasUSBIPProblem52(out) {
			return true, true
		}
	}
	if out, _, err := r.Run(ctx, pnputil, "/enum-devices"); err == nil || out != "" {
		if usbipDeviceRe.MatchString(out) {
			return true, false
		}
	}
	return false, false
}
