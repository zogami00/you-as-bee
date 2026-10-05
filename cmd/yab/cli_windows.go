//go:build windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/zogami00/you-as-bee/internal/api"
	"github.com/zogami00/you-as-bee/internal/client"
	"github.com/zogami00/you-as-bee/internal/config"
	"github.com/zogami00/you-as-bee/internal/elevate"
	"github.com/zogami00/you-as-bee/internal/execx"
	"github.com/zogami00/you-as-bee/internal/proto"
	"github.com/zogami00/you-as-bee/internal/usbipwin"
)

// noopUSBIP satisfies client.USBIP when usbip.exe is unavailable, so read-only
// commands still work.
type noopUSBIP struct{}

func (noopUSBIP) Attach(context.Context, string, string) error {
	return errors.New("usbip.exe not available")
}
func (noopUSBIP) Detach(context.Context, int) error {
	return errors.New("usbip.exe not available")
}
func (noopUSBIP) Port(context.Context) ([]usbipwin.PortEntry, error) { return nil, nil }
func (noopUSBIP) ListRemote(context.Context, string) ([]usbipwin.RemoteDevice, error) {
	return nil, nil
}

// deviceRow is the CLI/JSON view of one agent device.
type deviceRow struct {
	Server  string `json:"server"`
	Pin     string `json:"pin"`
	BusID   string `json:"busid"`
	VID     string `json:"vid"`
	PID     string `json:"pid"`
	State   string `json:"state"`
	Present bool   `json:"present"`
	Mode    string `json:"mode"`
}

func cmdList(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("list")
	configPath := fs.String("config", defaultConfigPath(), "path to client config")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, err := loadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "yab list: %v\n", err)
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	rows := make([]deviceRow, 0)
	for _, s := range cfg.Servers {
		cli := api.NewClient(apiBase(cfg, s), s.Token, cfg.CommandTimeout.Duration())
		devs, derr := cli.Devices(ctx)
		if derr != nil {
			fmt.Fprintf(stderr, "yab list: %s: %v\n", s.Name, derr)
			continue
		}
		for _, d := range devs {
			rows = append(rows, deviceRow{s.Name, d.Pin, d.BusID, d.VID, d.PID, d.State, d.Present, d.Mode})
		}
	}

	if *asJSON {
		return writeJSON(stdout, stderr, rows)
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SERVER\tPIN\tBUSID\tVID:PID\tSTATE\tPRESENT\tMODE")
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s:%s\t%s\t%v\t%s\n",
			r.Server, r.Pin, r.BusID, r.VID, r.PID, r.State, r.Present, r.Mode)
	}
	_ = tw.Flush()
	return 0
}

func cmdStatus(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("status")
	configPath := fs.String("config", defaultConfigPath(), "path to client config")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, err := loadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "yab status: %v\n", err)
		return 1
	}

	tool, terr := locateTool(cfg)
	if terr != nil {
		fmt.Fprintf(stderr, "yab status: warning: %v\n", terr)
	}
	m, err := newManager(cfg, tool)
	if err != nil {
		fmt.Fprintf(stderr, "yab status: %v\n", err)
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	servers := m.CheckServers(ctx)
	var ports []usbipwin.PortEntry
	if tool != nil {
		if p, perr := tool.Port(ctx); perr == nil {
			ports = p
		}
	}

	if *asJSON {
		return writeJSON(stdout, stderr, struct {
			Servers []client.ServerStatus `json:"servers"`
			Ports   []usbipwin.PortEntry  `json:"ports"`
		}{servers, ports})
	}

	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SERVER\tHOST:PORT\tREACHABLE\tTOKEN\tDEVICES\tERROR")
	for _, s := range servers {
		fmt.Fprintf(tw, "%s\t%s:%d\t%v\t%v\t%d\t%s\n",
			s.Name, s.Host, s.APIPort, s.Reachable, s.TokenValid, s.DeviceCount, s.Err)
	}
	_ = tw.Flush()

	fmt.Fprintln(stdout)
	if len(ports) == 0 {
		fmt.Fprintln(stdout, "no devices attached")
		return 0
	}
	tw2 := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw2, "PORT\tBUSID\tVID:PID\tREMOTE")
	for _, p := range ports {
		fmt.Fprintf(tw2, "%d\t%s\t%s:%s\t%s:%d\n", p.Port, p.BusID, p.VID, p.PID, p.Host, p.RemotePort)
	}
	_ = tw2.Flush()
	return 0
}

func cmdAttach(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("attach")
	configPath := fs.String("config", defaultConfigPath(), "path to client config")
	all := fs.Bool("all", false, "attach every configured device")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if !requireElevated(stderr) {
		return 1
	}
	cfg, err := loadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "yab attach: %v\n", err)
		return 1
	}
	tool, err := locateTool(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "yab attach: %v\n", err)
		return 1
	}

	targets, code := attachTargets(cfg, *all, fs.Args(), stderr)
	if code != 0 {
		return code
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	failures := 0
	for _, device := range targets {
		if err := attachDevice(ctx, cfg, tool, device); err != nil {
			fmt.Fprintf(stderr, "yab attach: %s: %v\n", device, err)
			failures++
			continue
		}
		fmt.Fprintf(stdout, "attached %s\n", device)
	}
	if failures > 0 {
		return 1
	}
	return 0
}

func cmdDetach(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("detach")
	configPath := fs.String("config", defaultConfigPath(), "path to client config")
	all := fs.Bool("all", false, "detach every configured device")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if !requireElevated(stderr) {
		return 1
	}
	cfg, err := loadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "yab detach: %v\n", err)
		return 1
	}
	tool, err := locateTool(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "yab detach: %v\n", err)
		return 1
	}

	targets, code := attachTargets(cfg, *all, fs.Args(), stderr)
	if code != 0 {
		return code
	}

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	failures := 0
	for _, device := range targets {
		if err := detachDevice(ctx, cfg, tool, device); err != nil {
			fmt.Fprintf(stderr, "yab detach: %s: %v\n", device, err)
			failures++
			continue
		}
		fmt.Fprintf(stdout, "detached %s\n", device)
	}
	if failures > 0 {
		return 1
	}
	return 0
}

// attachTargets resolves the device list for attach/detach.
func attachTargets(cfg config.ClientConfig, all bool, args []string, stderr io.Writer) ([]string, int) {
	if all {
		out := make([]string, 0, len(cfg.AutoAttach))
		for _, a := range cfg.AutoAttach {
			out = append(out, a.Device)
		}
		if len(out) == 0 {
			fmt.Fprintln(stderr, "yab: no auto_attach devices configured")
			return nil, 1
		}
		return out, 0
	}
	if len(args) != 1 {
		fmt.Fprintln(stderr, "yab: expected one device, or --all")
		return nil, 2
	}
	return []string{args[0]}, 0
}

// attachDevice ensures one configured device is exported and attached, using
// the bus id the agent reports at that moment.
func attachDevice(ctx context.Context, cfg config.ClientConfig, tool *usbipwin.Tool, device string) error {
	a, ok := autoAttachFor(cfg, device)
	if !ok {
		return fmt.Errorf("unknown device %q (not in auto_attach)", device)
	}
	s, ok := serverByHost(cfg, a.Server)
	if !ok {
		return fmt.Errorf("unknown server %q", a.Server)
	}
	cli := api.NewClient(apiBase(cfg, s), s.Token, cfg.CommandTimeout.Duration())

	dev, err := cli.Device(ctx, device)
	if err != nil {
		return fmt.Errorf("%s: %w", s.Name, err)
	}
	if !dev.Present {
		return fmt.Errorf("%s: device is not present on the Pi", s.Name)
	}
	if dev.Mode == config.ModeOnDemand && dev.State != proto.StateExported && dev.State != proto.StateInUse {
		if err := cli.Export(ctx, dev.Pin, false); err != nil {
			return fmt.Errorf("export: %w", err)
		}
		if refreshed, rerr := cli.Device(ctx, device); rerr == nil {
			dev = refreshed
		}
	}
	if dev.BusID == "" {
		return fmt.Errorf("%s: device has no bus id", s.Name)
	}

	if ports, perr := tool.Port(ctx); perr == nil {
		for _, p := range ports {
			if p.BusID == dev.BusID && strings.EqualFold(p.Host, s.Host) {
				return nil // already attached
			}
		}
	}

	if err := tool.Attach(ctx, s.Host, dev.BusID); err != nil {
		return err
	}
	ports, perr := tool.Port(ctx)
	if perr != nil {
		return nil // attach reported success; confirmation unavailable
	}
	for _, p := range ports {
		if p.BusID == dev.BusID && strings.EqualFold(p.Host, s.Host) {
			return nil
		}
	}
	return errors.New("usbip attach reported success but the port did not appear")
}

// detachDevice detaches any local port for the device's server and bus id.
func detachDevice(ctx context.Context, cfg config.ClientConfig, tool *usbipwin.Tool, device string) error {
	a, ok := autoAttachFor(cfg, device)
	if !ok {
		return fmt.Errorf("unknown device %q (not in auto_attach)", device)
	}
	s, ok := serverByHost(cfg, a.Server)
	if !ok {
		return fmt.Errorf("unknown server %q", a.Server)
	}

	var busid string
	cli := api.NewClient(apiBase(cfg, s), s.Token, cfg.CommandTimeout.Duration())
	if dev, err := cli.Device(ctx, device); err == nil {
		busid = dev.BusID
	}

	ports, err := tool.Port(ctx)
	if err != nil {
		return err
	}
	detached := 0
	for _, p := range ports {
		if !strings.EqualFold(p.Host, s.Host) {
			continue
		}
		if busid != "" && p.BusID != busid {
			continue
		}
		if derr := tool.Detach(ctx, p.Port); derr != nil {
			return derr
		}
		detached++
	}
	if detached == 0 {
		return fmt.Errorf("no attached port for %q on %s", device, s.Name)
	}
	return nil
}

// doctorReport is the JSON shape of `yab doctor`.
type doctorReport struct {
	UsbipPath      string                `json:"usbip_path"`
	UsbipFound     bool                  `json:"usbip_found"`
	UsbipVersion   string                `json:"usbip_version,omitempty"`
	DriverPresent  bool                  `json:"driver_present"`
	DriverUnsigned bool                  `json:"driver_unsigned"`
	Elevated       bool                  `json:"elevated"`
	TestSigning    string                `json:"test_signing"`
	SecureBoot     string                `json:"secure_boot"`
	Servers        []client.ServerStatus `json:"servers"`
}

func cmdDoctor(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("doctor")
	configPath := fs.String("config", defaultConfigPath(), "path to client config")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	rep := doctorReport{TestSigning: "unknown", SecureBoot: "unknown"}
	runner := execx.New()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cfg, cfgErr := loadConfig(*configPath)
	if cfgErr != nil {
		fmt.Fprintf(stderr, "yab doctor: %v\n", cfgErr)
	}

	path, err := usbipwin.Locate(cfg.UsbipPath)
	if err != nil {
		fmt.Fprintf(stderr, "yab doctor: %v\n", err)
	} else {
		rep.UsbipFound = true
		rep.UsbipPath = path
		rep.UsbipVersion = usbipVersion(ctx, runner, path)
		rep.DriverPresent, rep.DriverUnsigned = usbipwin.DriverStatus(ctx, runner, path)
	}
	rep.Elevated = elevate.IsElevated()
	rep.TestSigning = testSigning(ctx, runner)
	rep.SecureBoot = secureBoot()

	if m, merr := newManager(cfg, nil); merr == nil {
		rep.Servers = m.CheckServers(ctx)
	}

	if *asJSON {
		return writeJSON(stdout, stderr, rep)
	}

	fmt.Fprintf(stdout, "usbip.exe:      %s\n", displayPath(rep))
	fmt.Fprintf(stdout, "usbip version:  %s\n", orUnknown(rep.UsbipVersion))
	fmt.Fprintf(stdout, "driver present: %v\n", rep.DriverPresent)
	fmt.Fprintf(stdout, "driver signed:  %s\n", signedLabel(rep))
	fmt.Fprintf(stdout, "elevated:       %v\n", rep.Elevated)
	fmt.Fprintf(stdout, "test signing:   %s\n", rep.TestSigning)
	fmt.Fprintf(stdout, "secure boot:    %s\n", rep.SecureBoot)
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SERVER\tREACHABLE\tTOKEN\tDEVICES\tERROR")
	for _, s := range rep.Servers {
		fmt.Fprintf(tw, "%s\t%v\t%v\t%d\t%s\n", s.Name, s.Reachable, s.TokenValid, s.DeviceCount, s.Err)
	}
	_ = tw.Flush()
	return 0
}

func displayPath(rep doctorReport) string {
	if !rep.UsbipFound {
		return "NOT FOUND"
	}
	return rep.UsbipPath
}

func signedLabel(rep doctorReport) string {
	switch {
	case !rep.DriverPresent:
		return "n/a (driver not found)"
	case rep.DriverUnsigned:
		return "NO (problem code 52)"
	default:
		return "yes"
	}
}

func usbipVersion(ctx context.Context, r execx.Runner, path string) string {
	for _, args := range [][]string{{"version"}, {"--version"}, {"-v"}} {
		out, errOut, err := r.Run(ctx, path, args...)
		if err == nil {
			text := strings.TrimSpace(out)
			if text == "" {
				text = strings.TrimSpace(errOut)
			}
			if text != "" {
				return firstLine(text)
			}
		}
	}
	return ""
}

// testSigning reads bcdedit output; it never changes the setting.
func testSigning(ctx context.Context, r execx.Runner) string {
	out, _, err := r.Run(ctx, "bcdedit", "/enum", "{current}")
	if err != nil {
		return "unknown"
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(strings.ToLower(line), "testsigning") {
			if strings.Contains(strings.ToLower(line), "yes") {
				return "enabled"
			}
			if strings.Contains(strings.ToLower(line), "no") {
				return "disabled"
			}
		}
	}
	return "unknown"
}

func orUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "unknown"
	}
	return s
}

func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

func writeJSON(stdout, stderr io.Writer, v any) int {
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		fmt.Fprintf(stderr, "yab: %v\n", err)
		return 1
	}
	return 0
}
