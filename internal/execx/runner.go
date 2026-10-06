// Package execx is a small, shell-free command runner shared by both binaries.
//
// Commands are always invoked directly with an argument vector; nothing is ever
// passed through a shell such as sh -c, so argument quoting cannot be abused.
package execx

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
)

// Runner runs an external program and captures its output.
//
// Implementations must honour ctx for cancellation and deadlines.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (stdout string, stderr string, err error)
}

// ExecRunner is the production Runner backed by os/exec.
type ExecRunner struct{}

// New returns a Runner backed by os/exec.
func New() *ExecRunner { return &ExecRunner{} }

// Run executes name with args, capturing stdout and stderr. A per-call timeout
// is taken from ctx via exec.CommandContext.
func (ExecRunner) Run(ctx context.Context, name string, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	// On Windows this suppresses a console window for console children
	// (usbip.exe) started from the windowless yabw.exe; it is nil elsewhere.
	cmd.SysProcAttr = childSysProcAttr()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// ExitCode extracts the process exit code from err.
//
// It returns 0 when err is nil and -1 when err is not an *exec.ExitError (for
// example when the binary was not found or the call was cancelled).
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}
