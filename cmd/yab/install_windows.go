//go:build windows

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zogami00/you-as-bee/internal/execx"
)

const taskName = "you-as-bee-client"

func cmdInstall(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("install")
	_ = fs.String("config", defaultConfigPath(), "path to client config")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if !requireElevated(stderr) {
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := doInstall(ctx, execx.New()); err != nil {
		fmt.Fprintf(stderr, "yab install: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "installed")
	return 0
}

func cmdUninstall(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("uninstall")
	_ = fs.String("config", defaultConfigPath(), "path to client config")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if !requireElevated(stderr) {
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := doUninstall(ctx, execx.New()); err != nil {
		fmt.Fprintf(stderr, "yab uninstall: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "uninstalled")
	return 0
}

// doInstall copies the binary, locks down the data directory and registers the
// logon task. It is idempotent.
func doInstall(ctx context.Context, r execx.Runner) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	pf := os.Getenv("ProgramFiles")
	if pf == "" {
		pf = `C:\Program Files`
	}
	dir := filepath.Join(pf, "you-as-bee")
	target := filepath.Join(dir, "yab.exe")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	// Stop a running tray before copying: a locked yab.exe cannot be replaced.
	stopClient(ctx, r)
	if !strings.EqualFold(exe, target) {
		if err := copyFile(exe, target); err != nil {
			return fmt.Errorf("copy binary: %w", err)
		}
	}

	dataDir := filepath.Join(programData(), "you-as-bee")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dataDir, err)
	}
	// Administrators (S-1-5-32-544) and SYSTEM (S-1-5-18) only.
	if _, se, err := r.Run(ctx, "icacls", dataDir,
		"/inheritance:r",
		"/grant:r", "*S-1-5-32-544:(OI)(CI)F",
		"/grant:r", "*S-1-5-18:(OI)(CI)F",
	); err != nil {
		return fmt.Errorf("icacls: %w: %s", err, strings.TrimSpace(se))
	}

	if err := registerTask(ctx, r, target); err != nil {
		return err
	}
	return nil
}

// registerTask creates the highest-privilege logon task through the
// ScheduledTasks module rather than `schtasks /TR`, which cannot express the
// settings below and whose argument quoting fails on PowerShell 5.1. The task
// needs an -AtLogOn trigger: without one it is registered but never fires, so
// the tray and supervisor would not start at logon. The settings stop Windows
// from killing the task after 72 hours or refusing to start it on battery, both
// of which would silently drop the supervisor.
func registerTask(ctx context.Context, r execx.Runner, exe string) error {
	script := fmt.Sprintf(
		"$a = New-ScheduledTaskAction -Execute %s -Argument 'tray'; "+
			"$t = New-ScheduledTaskTrigger -AtLogOn; "+
			"$s = New-ScheduledTaskSettingsSet -ExecutionTimeLimit 0 "+
			"-AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -StartWhenAvailable; "+
			"Register-ScheduledTask -TaskName %s -Action $a -Trigger $t -Settings $s "+
			"-RunLevel Highest -Force | Out-Null",
		psSingleQuote(exe), psSingleQuote(taskName))
	if _, se, err := r.Run(ctx, "powershell.exe",
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-Command", script); err != nil {
		return fmt.Errorf("register task: %w: %s", err, strings.TrimSpace(se))
	}
	return nil
}

// psSingleQuote quotes s as a PowerShell single-quoted literal.
func psSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// stopClient ends the logon task and any running yab.exe other than this
// process, so install can overwrite the binary and uninstall can remove it.
// An in-place install/uninstall must not kill itself mid-run.
func stopClient(ctx context.Context, r execx.Runner) {
	_, _, _ = r.Run(ctx, "schtasks", "/End", "/TN", taskName)
	_, _, _ = r.Run(ctx, "taskkill", "/IM", "yab.exe", "/F",
		"/FI", fmt.Sprintf("PID ne %d", os.Getpid()))
}

// doUninstall removes the logon task and the installed binary directory. The
// config in %ProgramData% is deliberately left in place.
func doUninstall(ctx context.Context, r execx.Runner) error {
	// Stop the tray first: Remove-Item fails on a locked yab.exe.
	stopClient(ctx, r)
	if _, se, err := r.Run(ctx, "schtasks", "/Delete", "/TN", taskName, "/F"); err != nil {
		// A missing task is fine; report any other failure.
		if !strings.Contains(strings.ToLower(se), "cannot find") {
			return fmt.Errorf("schtasks delete: %w: %s", err, strings.TrimSpace(se))
		}
	}
	pf := os.Getenv("ProgramFiles")
	if pf == "" {
		pf = `C:\Program Files`
	}
	dir := filepath.Join(pf, "you-as-bee")
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove %s: %w", dir, err)
	}
	return nil
}

// copyFile writes src to dst via a temporary file so a running binary is not
// truncated in place.
func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	tmp := dst + ".new"
	if err := os.WriteFile(tmp, data, 0o755); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}
