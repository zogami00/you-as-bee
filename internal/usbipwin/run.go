package usbipwin

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/zogami00/you-as-bee/internal/execx"
)

// Command timeouts. Attach can wait for the driver to enumerate a device over
// the network, so it gets a longer budget than the read-only commands.
const (
	attachTimeout  = 20 * time.Second
	defaultTimeout = 10 * time.Second
)

// DefaultReceiveMode is the usbip-win2 value passed to --receive-mode when
// Tool.ReceiveMode is empty. low-latency avoids the zero-copy default, which on
// real hardware produced ~23x more Windows device-change events and continuous
// USB stall/reset messages in the Pi kernel log.
const DefaultReceiveMode = "low-latency"

// busIDRe is the only bus-id shape usbip-win2 accepts.
var busIDRe = regexp.MustCompile(`^[0-9]+-[0-9]+(?:\.[0-9]+)*$`)

// ErrInvalidBusID is returned when a bus id is not of the form "1-1.4".
var ErrInvalidBusID = errors.New("usbipwin: invalid bus id")

// ErrInvalidHost is returned when a host would be parsed by usbip.exe as an
// option rather than a value.
var ErrInvalidHost = errors.New("usbipwin: invalid host")

// validHost rejects an empty host or one beginning with "-", which usbip.exe
// would read as a command-line option instead of the -r argument value.
func validHost(host string) bool {
	host = strings.TrimSpace(host)
	return host != "" && !strings.HasPrefix(host, "-")
}

// Tool drives one usbip.exe binary. Every command is invoked directly with an
// argument vector; nothing is ever passed through a shell.
type Tool struct {
	// Path is the path to usbip.exe.
	Path string
	// ReceiveMode is the value passed to attach --receive-mode. Empty means
	// DefaultReceiveMode (low-latency).
	ReceiveMode string
	// Runner runs the binary. A nil Runner defaults to execx.ExecRunner{}.
	Runner execx.Runner
}

// New returns a Tool for path backed by r. A nil r uses the os/exec runner.
func New(path string, r execx.Runner) *Tool {
	if r == nil {
		r = execx.ExecRunner{}
	}
	return &Tool{Path: path, Runner: r}
}

func (t *Tool) run(ctx context.Context, timeout time.Duration, args ...string) (string, string, error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return t.Runner.Run(cctx, t.Path, args...)
}

// Attach attaches the remote device at host/busid to a local vhci port via
// `usbip attach -r <host> -b <busid> --once --receive-mode <mode>`.
//
// --once is essential: without it usbip-win2's driver starts its own endless
// reconnect loop, so a client that is offline or has unplugged the dongle is
// still hammered with import requests (26 in ~30 min were observed with no
// usbip process running), each re-enumerating a USB device.
func (t *Tool) Attach(ctx context.Context, host, busid string) error {
	if !validHost(host) {
		return fmt.Errorf("%w: %q", ErrInvalidHost, host)
	}
	if !busIDRe.MatchString(busid) {
		return fmt.Errorf("%w: %q", ErrInvalidBusID, busid)
	}
	mode := t.ReceiveMode
	if mode == "" {
		mode = DefaultReceiveMode
	}
	_, stderr, err := t.run(ctx, attachTimeout,
		"attach", "-r", host, "-b", busid, "--once", "--receive-mode", mode)
	if err == nil {
		return nil
	}
	if code := execx.ExitCode(err); code != 0 {
		return ClassifyFailure(ctx, t.Runner, t.Path, code, stderr)
	}
	// The command did not run at all (binary missing, cancelled).
	return fmt.Errorf("usbipwin: attach %s on %s: %w", busid, host, err)
}

// Detach detaches the local vhci port via `usbip detach -p <port>`, then clears
// any automatic attach retry the driver may still be running via
// `usbip attach --stop-all`.
//
// New attaches pass --once, but a retry started by an earlier version lives in
// the driver, not in a usbip process, so detaching the port is not enough to
// stop it. --stop-all is best effort: the port is already detached, so its
// failure must not turn a successful detach into an error.
func (t *Tool) Detach(ctx context.Context, port int) error {
	if port < 0 {
		return fmt.Errorf("usbipwin: invalid port %d", port)
	}
	_, stderr, err := t.run(ctx, defaultTimeout, "detach", "-p", strconv.Itoa(port))
	if err != nil {
		return fmt.Errorf("usbipwin: detach port %d: %w: %s", port, err, strings.TrimSpace(stderr))
	}
	_, _, _ = t.run(ctx, defaultTimeout, "attach", "--stop-all")
	return nil
}

// Port returns the local vhci port list via `usbip port`.
func (t *Tool) Port(ctx context.Context) ([]PortEntry, error) {
	stdout, stderr, err := t.run(ctx, defaultTimeout, "port")
	if err != nil {
		return nil, fmt.Errorf("usbipwin: port: %w: %s", err, strings.TrimSpace(stderr))
	}
	return ParsePort(stdout)
}

// ListRemote lists the devices host exports via `usbip list -r <host>`.
func (t *Tool) ListRemote(ctx context.Context, host string) ([]RemoteDevice, error) {
	if !validHost(host) {
		return nil, fmt.Errorf("%w: %q", ErrInvalidHost, host)
	}
	stdout, stderr, err := t.run(ctx, defaultTimeout, "list", "-r", host)
	if err != nil {
		return nil, fmt.Errorf("usbipwin: list -r %s: %w: %s", host, err, strings.TrimSpace(stderr))
	}
	return ParseRemote(stdout)
}
