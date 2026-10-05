//go:build !linux

package main

// isRoot reports whether the process is running as root. usbip-host only
// exists on Linux, so every check is reported as failing here.
func isRoot() bool { return false }
