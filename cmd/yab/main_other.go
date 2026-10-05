//go:build !windows

// Command yab is the Windows USB-over-LAN client. It only runs on Windows; on
// every other platform this stub reports that and exits non-zero.
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "yab is Windows-only")
	os.Exit(1)
}
