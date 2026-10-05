// Command yabd is the Raspberry Pi USB-over-LAN agent.
//
// It pins USB devices, exports them with the in-kernel usbip-host driver, and
// serves a small management API on port 3241. The reconcile loop owns every
// sysfs write; the API only records intent.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/zogami00/you-as-bee/internal/version"
)

const defaultConfigPath = "/etc/you-as-bee/agent.json"

func main() {
	os.Exit(dispatch(os.Args[1:], os.Stdout, os.Stderr))
}

func dispatch(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stderr)
		return 2
	}
	switch args[0] {
	case "run":
		return cmdRun(args[1:], stdout, stderr)
	case "list":
		return cmdList(args[1:], stdout, stderr)
	case "status":
		return cmdStatus(args[1:], stdout, stderr)
	case "export":
		return cmdExport(args[1:], stdout, stderr)
	case "unexport":
		return cmdUnexport(args[1:], stdout, stderr)
	case "pin":
		return cmdPin(args[1:], stdout, stderr)
	case "reset":
		return cmdReset(args[1:], stdout, stderr)
	case "doctor":
		return cmdDoctor(args[1:], stdout, stderr)
	case "version":
		fmt.Fprintf(stdout, "yabd %s\n", version.String())
		return 0
	case "-h", "--help", "help":
		printUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "yabd: unknown command %q\n\n", args[0])
		printUsage(stderr)
		return 2
	}
}

func printUsage(w io.Writer) {
	fmt.Fprint(w, `yabd - USB-over-LAN Raspberry Pi agent

Usage:
  yabd run      [--config PATH] [--listen ADDR] [--log-level L] [--log-format F]
  yabd list     [--json] [--all] [--root PATH]
  yabd status   [--config PATH] [--url URL] [--json]
  yabd export   [--config PATH] [--url URL] [--persist] [--force] <selector>
  yabd unexport [--config PATH] [--url URL] <selector>
  yabd pin      [--config PATH] --name NAME <busid>
  yabd reset    [--config PATH] [--url URL] <name>
  yabd doctor   [--config PATH]
  yabd version

A selector is a pin name, "vid:pid[:serial]", or a USB bus id such as 1-1.4.
`)
}

// newFlagSet returns a FlagSet that does not print usage on error.
func newFlagSet(name string) *flag.FlagSet {
	return flag.NewFlagSet(name, flag.ContinueOnError)
}
