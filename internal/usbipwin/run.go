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

// busIDRe is the only bus-id shape usbip-win2 accepts.
var busIDRe = regexp.MustCompile(`^[0-9]+-[0-9]+(?:\.[0-9]+)*$`)

// ErrInvalidBusID is returned when a bus id is not of the form "1-1.4".
var ErrInvalidBusID = errors.New("usbipwin: invalid bus id")

// Tool drives one usbip.exe binary. Every command is invoked directly with an
// argument vector; nothing is ever passed through a shell.
type Tool struct {
	// Path is the path to usbip.exe.
	Path string
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
// `usbip attach -r <host> -b <busid>`.
func (t *Tool) Attach(ctx context.Context, host, busid string) error {
	if !busIDRe.MatchString(busid) {
		return fmt.Errorf("%w: %q", ErrInvalidBusID, busid)
	}
	_, stderr, err := t.run(ctx, attachTimeout, "attach", "-r", host, "-b", busid)
	if err == nil {
		return nil
	}
	if code := execx.ExitCode(err); code != 0 {
		return ClassifyFailure(ctx, t.Runner, t.Path, code, stderr)
	}
	// The command did not run at all (binary missing, cancelled).
	return fmt.Errorf("usbipwin: attach %s on %s: %w", busid, host, err)
}

// Detach detaches the local vhci port via `usbip detach -p <port>`.
func (t *Tool) Detach(ctx context.Context, port int) error {
	if port < 0 {
		return fmt.Errorf("usbipwin: invalid port %d", port)
	}
	_, stderr, err := t.run(ctx, defaultTimeout, "detach", "-p", strconv.Itoa(port))
	if err != nil {
		return fmt.Errorf("usbipwin: detach port %d: %w: %s", port, err, strings.TrimSpace(stderr))
	}
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
	stdout, stderr, err := t.run(ctx, defaultTimeout, "list", "-r", host)
	if err != nil {
		return nil, fmt.Errorf("usbipwin: list -r %s: %w: %s", host, err, strings.TrimSpace(stderr))
	}
	return ParseRemote(stdout)
}
