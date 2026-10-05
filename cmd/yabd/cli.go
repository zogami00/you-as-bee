package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/zogami00/you-as-bee/internal/api"
	"github.com/zogami00/you-as-bee/internal/config"
	"github.com/zogami00/you-as-bee/internal/proto"
	"github.com/zogami00/you-as-bee/internal/sysfs"
	"github.com/zogami00/you-as-bee/internal/usbiphost"
)

const defaultSysfsRoot = "/sys/bus/usb/devices"

func cmdList(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("list")
	asJSON := fs.Bool("json", false, "emit JSON")
	all := fs.Bool("all", false, "include every enumerated entry")
	root := fs.String("root", defaultSysfsRoot, "sysfs USB devices directory")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	_ = all

	devs, err := sysfs.Enumerate(sysfs.OSFS{}, *root)
	if err != nil {
		fmt.Fprintf(stderr, "yabd list: %v\n", err)
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(devs); err != nil {
			fmt.Fprintf(stderr, "yabd list: %v\n", err)
			return 1
		}
		return 0
	}

	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "BUSID\tVID:PID\tSERIAL\tDRIVER\tSTATUS\tPRODUCT")
	for _, d := range devs {
		fmt.Fprintf(tw, "%s\t%s:%s\t%s\t%s\t%d\t%s\n",
			d.BusID, d.VID, d.PID, d.Serial, d.Driver, d.Status, d.Product)
	}
	_ = tw.Flush()
	return 0
}

func cmdStatus(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("status")
	configPath := fs.String("config", defaultConfigPath, "path to the agent config file")
	rawURL := fs.String("url", "", "agent API base URL")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	client, err := newAPIClient(*configPath, *rawURL)
	if err != nil {
		fmt.Fprintf(stderr, "yabd status: %v\n", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	info, err := client.Info(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "yabd status: %v\n", err)
		return 1
	}
	devs, err := client.Devices(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "yabd status: %v\n", err)
		return 1
	}

	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(struct {
			Info    proto.Info     `json:"info"`
			Devices []proto.Device `json:"devices"`
		}{info, devs}); err != nil {
			fmt.Fprintf(stderr, "yabd status: %v\n", err)
			return 1
		}
		return 0
	}

	fmt.Fprintf(stdout, "version:   %s\n", info.Version)
	fmt.Fprintf(stdout, "hostname:  %s\n", info.Hostname)
	fmt.Fprintf(stdout, "uptime:    %ds\n", info.UptimeSec)
	fmt.Fprintf(stdout, "usbipd:    %v\n", info.UsbipdUp)
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PIN\tBUSID\tVID:PID\tSTATE\tPRESENT\tMODE")
	for _, d := range devs {
		fmt.Fprintf(tw, "%s\t%s\t%s:%s\t%s\t%v\t%s\n", d.Pin, d.BusID, d.VID, d.PID, d.State, d.Present, d.Mode)
	}
	_ = tw.Flush()
	return 0
}

func cmdExport(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("export")
	configPath := fs.String("config", defaultConfigPath, "path to the agent config file")
	rawURL := fs.String("url", "", "agent API base URL")
	persist := fs.Bool("persist", false, "make the pin export on every boot")
	force := fs.Bool("force", false, "disturb an attached client")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "yabd export: expected one selector")
		return 2
	}
	sel, err := parseSelector(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(stderr, "yabd export: %v\n", err)
		return 2
	}

	if *persist {
		if err := setPinMode(*configPath, sel, config.ModeAlways); err != nil {
			fmt.Fprintf(stderr, "yabd export --persist: %v\n", err)
			return 1
		}
	}

	// Prefer the running agent so we cannot race the reconciler.
	client, cerr := newAPIClient(*configPath, *rawURL)
	if cerr == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := client.Export(ctx, sel.raw, *force)
		cancel()
		if err == nil {
			fmt.Fprintf(stdout, "exported %s\n", sel.raw)
			return 0
		}
		if reachedAPI(err) {
			fmt.Fprintf(stderr, "yabd export: %v\n", err)
			return 1
		}
	}

	return directBind(*configPath, sel, *force, stdout, stderr)
}

func cmdUnexport(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("unexport")
	configPath := fs.String("config", defaultConfigPath, "path to the agent config file")
	rawURL := fs.String("url", "", "agent API base URL")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "yabd unexport: expected one selector")
		return 2
	}
	sel, err := parseSelector(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(stderr, "yabd unexport: %v\n", err)
		return 2
	}

	client, cerr := newAPIClient(*configPath, *rawURL)
	if cerr == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := client.Unexport(ctx, sel.raw)
		cancel()
		if err == nil {
			fmt.Fprintf(stdout, "unexported %s\n", sel.raw)
			return 0
		}
		if reachedAPI(err) {
			fmt.Fprintf(stderr, "yabd unexport: %v\n", err)
			return 1
		}
	}
	return directUnbind(*configPath, sel, stdout, stderr)
}

func cmdReset(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("reset")
	configPath := fs.String("config", defaultConfigPath, "path to the agent config file")
	rawURL := fs.String("url", "", "agent API base URL")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "yabd reset: expected one pin name")
		return 2
	}
	client, err := newAPIClient(*configPath, *rawURL)
	if err != nil {
		fmt.Fprintf(stderr, "yabd reset: %v\n", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := client.Reset(ctx, fs.Arg(0)); err != nil {
		fmt.Fprintf(stderr, "yabd reset: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "reset %s\n", fs.Arg(0))
	return 0
}

func cmdPin(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("pin")
	configPath := fs.String("config", defaultConfigPath, "path to the agent config file")
	name := fs.String("name", "", "pin name (^[a-z0-9-]{1,32}$)")
	root := fs.String("root", defaultSysfsRoot, "sysfs USB devices directory")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 || *name == "" {
		fmt.Fprintln(stderr, "yabd pin: usage: yabd pin --name NAME <busid>")
		return 2
	}
	sel, err := parseSelector(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(stderr, "yabd pin: %v\n", err)
		return 2
	}

	var cfg config.AgentConfig
	if err := config.Load(*configPath, &cfg); err != nil {
		fmt.Fprintf(stderr, "yabd pin: %v\n", err)
		return 1
	}
	dev, _, err := resolveDevice(sysfs.OSFS{}, *root, cfg.Devices, sel)
	if err != nil {
		fmt.Fprintf(stderr, "yabd pin: %v\n", err)
		return 1
	}
	for _, d := range cfg.Devices {
		if d.Name == *name {
			fmt.Fprintf(stderr, "yabd pin: pin %q already exists\n", *name)
			return 1
		}
	}
	cfg.Devices = append(cfg.Devices, config.DeviceConfig{
		Name:   *name,
		VID:    strings.ToLower(dev.VID),
		PID:    strings.ToLower(dev.PID),
		Serial: dev.Serial,
		Mode:   config.ModeOnDemand,
	})
	if err := saveConfig(*configPath, &cfg); err != nil {
		fmt.Fprintf(stderr, "yabd pin: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "pinned %s as %q\n", dev.BusID, *name)
	return 0
}

// setPinMode updates an existing pin, or adds it, with the given mode.
func setPinMode(configPath string, sel selector, mode string) error {
	var cfg config.AgentConfig
	if err := config.Load(configPath, &cfg); err != nil {
		return err
	}
	for i := range cfg.Devices {
		if cfg.Devices[i].Name == sel.raw {
			cfg.Devices[i].Mode = mode
			return saveConfig(configPath, &cfg)
		}
	}
	// Not a named pin: pin the selected device under a derived name.
	dev, _, err := resolveDevice(sysfs.OSFS{}, defaultSysfsRoot, cfg.Devices, sel)
	if err != nil {
		return err
	}
	name := deriveName(dev)
	if name == "" {
		name = strings.ToLower(dev.VID + "-" + dev.PID)
	}
	cfg.Devices = append(cfg.Devices, config.DeviceConfig{
		Name: name, VID: strings.ToLower(dev.VID), PID: strings.ToLower(dev.PID),
		Serial: dev.Serial, Mode: mode,
	})
	return saveConfig(configPath, &cfg)
}

func deriveName(dev sysfs.Device) string {
	if dev.Product == "" {
		return ""
	}
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(dev.Product) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		case !prevDash && b.Len() > 0:
			b.WriteByte('-')
			prevDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func directBind(configPath string, sel selector, force bool, stdout, stderr io.Writer) int {
	var cfg config.AgentConfig
	if err := config.Load(configPath, &cfg); err != nil {
		fmt.Fprintf(stderr, "yabd export: %v\n", err)
		return 1
	}
	dev, _, err := resolveDevice(sysfs.OSFS{}, defaultSysfsRoot, cfg.Devices, sel)
	if err != nil {
		fmt.Fprintf(stderr, "yabd export: %v\n", err)
		return 1
	}
	binder := usbiphost.New()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := binder.Bind(ctx, dev, usbiphost.Options{Force: force}); err != nil {
		fmt.Fprintf(stderr, "yabd export: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "exported %s\n", dev.BusID)
	return 0
}

func directUnbind(configPath string, sel selector, stdout, stderr io.Writer) int {
	var cfg config.AgentConfig
	if err := config.Load(configPath, &cfg); err != nil {
		fmt.Fprintf(stderr, "yabd unexport: %v\n", err)
		return 1
	}
	dev, _, err := resolveDevice(sysfs.OSFS{}, defaultSysfsRoot, cfg.Devices, sel)
	if err != nil {
		fmt.Fprintf(stderr, "yabd unexport: %v\n", err)
		return 1
	}
	binder := usbiphost.New()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := binder.Unbind(ctx, dev); err != nil {
		fmt.Fprintf(stderr, "yabd unexport: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "unexported %s\n", dev.BusID)
	return 0
}

// newAPIClient builds a client from the config file and an optional URL
// override.
func newAPIClient(configPath, rawURL string) (*api.Client, error) {
	var cfg config.AgentConfig
	if err := config.Load(configPath, &cfg); err != nil {
		return nil, err
	}
	token, err := readToken(cfg.TokenFile)
	if err != nil {
		return nil, err
	}
	base := rawURL
	if base == "" {
		host, port, err := net.SplitHostPort(cfg.Listen)
		if err != nil {
			host, port = "127.0.0.1", "3241"
		}
		if host == "" || host == "0.0.0.0" || host == "::" {
			host = "127.0.0.1"
		}
		base = "http://" + net.JoinHostPort(host, port)
	}
	return api.NewClient(base, token, 10*time.Second), nil
}

// reachedAPI reports whether err came from the API (as opposed to a connection
// failure), meaning there is no point trying a direct action.
func reachedAPI(err error) bool {
	var ae *api.APIError
	if errors.As(err, &ae) {
		return true
	}
	var ue *url.Error
	return errors.As(err, &ue) && ue.Err != nil && !isConnError(ue.Err)
}

func isConnError(err error) bool {
	var op *net.OpError
	return errors.As(err, &op)
}

func saveConfig(path string, cfg *config.AgentConfig) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o600)
}
