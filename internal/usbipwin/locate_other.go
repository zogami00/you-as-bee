//go:build !windows

package usbipwin

import (
	"errors"
	"fmt"
)

// ErrToolNotFound is returned when usbip.exe cannot be located. usbip-win2
// only exists on Windows, so this is always the result off Windows.
var ErrToolNotFound = errors.New("usbip-win2 usbip.exe not found - install usbip-win2 or set usbip_path in the client config")

// Locate is unsupported off Windows.
func Locate(explicit string) (string, error) {
	return "", fmt.Errorf("%w (usbip-win2 is Windows-only)", ErrToolNotFound)
}
