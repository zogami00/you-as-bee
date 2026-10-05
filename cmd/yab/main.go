// Command yab is the Windows USB-over-LAN client.
//
// TODO(milestone-2): implement usbip-win2 discovery and invocation, the device
// supervisor with reconnect/backoff, auto-attach, the CLI and the system-tray
// application. This is a scaffold stub that only reports the build version so
// the branch always compiles and the CI matrix is exercised.
package main

import (
	"fmt"
	"os"

	"github.com/zogami00/you-as-bee/internal/version"
)

func main() {
	fmt.Fprintf(os.Stdout, "yab %s\n", version.String())
	os.Exit(0)
}
