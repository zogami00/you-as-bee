// Command yabd is the Raspberry Pi USB-over-LAN agent.
//
// TODO(milestone-1): implement sysfs USB enumeration, driver bind/unbind, the
// reconcile loop, the management API on :3241, USB/IP export via usbip/usbipd,
// sd_notify and the systemd unit. This is a scaffold stub that only reports the
// build version so the branch always compiles and the CI matrix is exercised.
package main

import (
	"fmt"
	"os"

	"github.com/zogami00/you-as-bee/internal/version"
)

func main() {
	fmt.Fprintf(os.Stdout, "yabd %s\n", version.String())
	os.Exit(0)
}
