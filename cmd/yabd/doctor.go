package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"strings"

	"github.com/zogami00/you-as-bee/internal/config"
)

const (
	modprobeConf = "/etc/modprobe.d/yab-modprobe.conf"
	udevRuleFile = "/etc/udev/rules.d/90-you-as-bee.rules"
)

func cmdDoctor(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("doctor")
	configPath := fs.String("config", defaultConfigPath, "path to the agent config file")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cfg := loadOrDefaultConfig(*configPath)
	usbipBin := orDefault(cfg.USBIP.Bin, "/usr/sbin/usbip")
	usbipdBin := orDefault(cfg.USBIP.UsbipdBin, "/usr/sbin/usbipd")

	d := &doctor{out: stdout}
	d.check("running as root", isRoot(), "usbip-host needs root to write sysfs")
	d.check("usbip-core module loaded", fileExists("/sys/module/usbip_core"), "modprobe usbip-core")
	d.check("usbip-host module loaded", fileExists("/sys/module/usbip_host"), "modprobe usbip-host")
	d.check("usbip-host driver present", fileExists("/sys/bus/usb/drivers/usbip-host"), "usbip-host driver directory")
	d.check("usbip binary", binaryExists(usbipBin), usbipBin)
	d.check("usbipd binary", binaryExists(usbipdBin), usbipdBin)
	d.check("usbipd listening on 3240", portListening("127.0.0.1:3240"), "USB/IP TCP port")
	d.check("modprobe blacklist installed", fileExists(modprobeConf), modprobeConf)
	d.check("udev rule installed", fileExists(udevRuleFile), udevRuleFile)

	btDriver := bluetoothDriver("/sys/class/bluetooth/hci0")
	d.check("onboard Bluetooth on hci_uart", btDriver == "hci_uart",
		fmt.Sprintf("hci0 driver is %q; btusb must be blacklisted only because onboard BT uses UART", btDriver))

	if d.failures > 0 {
		fmt.Fprintf(stderr, "yabd doctor: %d check(s) failed\n", d.failures)
		return 1
	}
	fmt.Fprintln(stdout, "all checks passed")
	return 0
}

type doctor struct {
	out      io.Writer
	failures int
}

func (d *doctor) check(name string, ok bool, detail string) {
	status := "OK  "
	if !ok {
		status = "FAIL"
		d.failures++
	}
	fmt.Fprintf(d.out, "[%s] %s (%s)\n", status, name, detail)
}

func loadOrDefaultConfig(path string) config.AgentConfig {
	var cfg config.AgentConfig
	if err := config.Load(path, &cfg); err != nil {
		return config.AgentConfig{}
	}
	return cfg
}

func orDefault(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

func fileExists(name string) bool {
	_, err := os.Stat(name)
	return err == nil
}

func binaryExists(name string) bool {
	if _, err := exec.LookPath(name); err == nil {
		return true
	}
	return fileExists(name)
}

// bluetoothDriver returns the base name of the driver bound to an hci device,
// or "" when it cannot be determined.
func bluetoothDriver(dir string) string {
	target, err := os.Readlink(path.Join(dir, "device", "driver"))
	if err != nil {
		return ""
	}
	return path.Base(target)
}
