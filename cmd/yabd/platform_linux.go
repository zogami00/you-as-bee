//go:build linux

package main

import "os"

// isRoot reports whether the process is running as root.
func isRoot() bool { return os.Geteuid() == 0 }
