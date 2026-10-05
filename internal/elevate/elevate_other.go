//go:build !windows

// Package elevate reports whether the current process is running elevated and
// can relaunch it through the UAC "runas" verb. Outside Windows none of that
// exists, so the package degrades to an unsupported error.
package elevate

import "errors"

// ErrUnsupported is returned by RelaunchElevated on platforms without UAC.
var ErrUnsupported = errors.New("elevate: not supported on this platform")

// IsElevated always reports false off Windows; there is no UAC elevation
// concept to inspect.
func IsElevated() bool { return false }

// RelaunchElevated is unsupported off Windows.
func RelaunchElevated(_ []string) error { return ErrUnsupported }
