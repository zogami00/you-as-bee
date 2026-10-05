//go:build windows

package usbipwin

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/sys/windows/registry"

	"github.com/zogami00/you-as-bee/internal/elevate"
)

// elevated reports whether the process is running elevated. It is a variable so
// tests can drive the user-writable-path policy without a real UAC token.
var elevated = elevate.IsElevated

// ErrToolNotFound is returned when usbip.exe cannot be located. Its message
// tells the user how to fix it.
var ErrToolNotFound = errors.New("usbip-win2 usbip.exe not found - install usbip-win2 or set usbip_path in the client config")

var usbipDisplayRe = regexp.MustCompile(`(?i)usbip`)

// Locate finds usbip.exe, in order:
//
//  1. the explicit config usbip_path (when non-empty),
//  2. the 64-bit uninstall registry keys, matching DisplayName ~ (?i)usbip and
//     reading InstallLocation,
//  3. %ProgramW6432%\USBip\usbip.exe then %ProgramFiles%\USBip\usbip.exe,
//  4. exec.LookPath.
//
// When the resolved path lives under %USERPROFILE% or %LOCALAPPDATA% a warning
// is logged: an elevated process should not run a user-writable binary.
func Locate(explicit string) (string, error) {
	if p := strings.TrimSpace(explicit); p != "" {
		if !fileExists(p) {
			return "", fmt.Errorf("%w (usbip_path %q does not exist)", ErrToolNotFound, p)
		}
		if err := checkUserWritable(p, true); err != nil {
			return "", err
		}
		return p, nil
	}

	if p, ok := fromRegistry(); ok {
		if err := checkUserWritable(p, false); err != nil {
			return "", err
		}
		return p, nil
	}

	for _, env := range []string{"ProgramW6432", "ProgramFiles"} {
		if root := os.Getenv(env); root != "" {
			p := filepath.Join(root, "USBip", "usbip.exe")
			if fileExists(p) {
				if err := checkUserWritable(p, false); err != nil {
					return "", err
				}
				return p, nil
			}
		}
	}

	if p, err := exec.LookPath("usbip.exe"); err == nil {
		if err := checkUserWritable(p, false); err != nil {
			return "", err
		}
		return p, nil
	}
	return "", ErrToolNotFound
}

// fromRegistry scans the 64-bit uninstall keys for a usbip-win2 entry and
// returns its install location joined with usbip.exe.
func fromRegistry() (string, bool) {
	const uninstall = `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall`
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, uninstall,
		registry.QUERY_VALUE|registry.ENUMERATE_SUB_KEYS|registry.WOW64_64KEY)
	if err != nil {
		return "", false
	}
	defer key.Close()

	names, err := key.ReadSubKeyNames(0)
	if err != nil {
		return "", false
	}
	for _, name := range names {
		sub, err := registry.OpenKey(key, name, registry.QUERY_VALUE|registry.WOW64_64KEY)
		if err != nil {
			continue
		}
		display, _, derr := sub.GetStringValue("DisplayName")
		if derr != nil || !usbipDisplayRe.MatchString(display) {
			sub.Close()
			continue
		}
		loc, _, verr := sub.GetStringValue("InstallLocation")
		sub.Close()
		if verr != nil || strings.TrimSpace(loc) == "" {
			continue
		}
		candidate := filepath.Join(strings.TrimSpace(loc), "usbip.exe")
		if fileExists(candidate) {
			return candidate, true
		}
	}
	return "", false
}

// userWritableRoot returns the user-writable root (USERPROFILE or
// LOCALAPPDATA) that contains p, or "" when p is outside both.
func userWritableRoot(p string) string {
	for _, env := range []string{"USERPROFILE", "LOCALAPPDATA"} {
		root := os.Getenv(env)
		if root == "" {
			continue
		}
		rel, err := filepath.Rel(root, p)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, `..\`) && !strings.HasPrefix(rel, "../") {
			return root
		}
	}
	return ""
}

// checkUserWritable enforces the policy for a user-writable usbip.exe.
//
// An explicit usbip_path is the operator's opt-in, so it is only warned about.
// A path discovered from the registry, Program Files or PATH is refused when
// the process is elevated: running a user-writable binary as administrator is a
// privilege-escalation vector. When not elevated the same path is only warned
// about, because there is no privilege to escalate.
func checkUserWritable(p string, explicit bool) error {
	root := userWritableRoot(p)
	if root == "" {
		return nil
	}
	if explicit {
		slog.Warn("usbip.exe explicitly configured from a user-writable location; "+
			"an elevated process should not run a user-writable binary",
			"path", p, "root", root)
		return nil
	}
	if elevated() {
		return fmt.Errorf("%w: %q resolved under %s, which is user-writable; "+
			"set usbip_path explicitly to accept this risk", ErrToolNotFound, p, root)
	}
	slog.Warn("usbip.exe resolved from a user-writable location; "+
		"an elevated process should not run a user-writable binary",
		"path", p, "root", root)
	return nil
}

func fileExists(name string) bool {
	info, err := os.Stat(name)
	return err == nil && !info.IsDir()
}
