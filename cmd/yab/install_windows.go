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

	taskCommand := fmt.Sprintf(`"%s" tray`, target)
	if _, se, err := r.Run(ctx, "schtasks", "/Create",
		"/TN", taskName,
		"/TR", taskCommand,
		"/SC", "ONLOGON",
		"/RL", "HIGHEST",
		"/F",
	); err != nil {
		return fmt.Errorf("schtasks: %w: %s", err, strings.TrimSpace(se))
	}
	return nil
}

// doUninstall removes the logon task and the installed binary directory. The
// config in %ProgramData% is deliberately left in place.
func doUninstall(ctx context.Context, r execx.Runner) error {
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
