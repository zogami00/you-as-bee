// Package usbiphost binds and unbinds USB devices to the in-kernel usbip-host
// driver, the kernel half of the USB/IP export.
//
// The bind sequence itself lives in this file and is platform independent: all
// filesystem access goes through the SysFS interface and all command execution
// through execx.Runner, so the exact sequence can be unit-tested on Windows
// with fakes. Only the construction of the production Binder is split by build
// tag (bind_linux.go / bind_other.go).
package usbiphost

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"strconv"
	"strings"
	"syscall"

	"github.com/zogami00/you-as-bee/internal/execx"
	"github.com/zogami00/you-as-bee/internal/sysfs"
)

// Typed failure modes. Callers use errors.Is to distinguish them.
var (
	// ErrUnsupported means the platform does not implement usbip-host.
	ErrUnsupported = errors.New("usbiphost: unsupported platform")
	// ErrNotRoot means the process is not running as root.
	ErrNotRoot = errors.New("usbiphost: must run as root")
	// ErrModuleMissing means usbip-host is not loaded and could not be loaded.
	ErrModuleMissing = errors.New("usbiphost: usbip-host module not available")
	// ErrInUse means a client is attached to the exported device.
	ErrInUse = errors.New("usbiphost: device is attached to a client")
	// ErrVerify means the post-bind verification failed.
	ErrVerify = errors.New("usbiphost: bind verification failed")
	// ErrNotFound means the device is no longer present.
	ErrNotFound = errors.New("usbiphost: device not found")
	// ErrRolledBack means the bind failed and the changes were rolled back.
	ErrRolledBack = errors.New("usbiphost: bind failed and was rolled back")
)

const (
	driverName     = "usbip-host"
	statusExported = 1
	statusAttached = 2
	statusError    = 3
)

// SysFS is the injectable filesystem used by the bind sequence. Every read and
// write the sequence performs goes through it.
type SysFS interface {
	ReadFile(path string) ([]byte, error)
	WriteFile(path string, data []byte) error
	ReadLink(path string) (string, error)
	Exists(path string) bool
}

// Options controls a single Bind call.
type Options struct {
	// Force permits disturbing a device that already has a client attached.
	Force bool
}

// Bind exports dev using a default Binder constructed by New. Off Linux it
// reports ErrUnsupported. Use a Binder directly when you need to inject a
// filesystem or runner.
func Bind(ctx context.Context, dev sysfs.Device, opts Options) error {
	return New().Bind(ctx, dev, opts)
}

// Unbind releases dev using a default Binder constructed by New.
func Unbind(ctx context.Context, dev sysfs.Device, opts Options) error {
	return New().Unbind(ctx, dev, opts)
}

// Binder performs the usbip-host bind/unbind sequence against a sysfs tree.
type Binder struct {
	// FS is the injectable filesystem. Required.
	FS SysFS
	// Runner executes modprobe during preflight. Required on Linux.
	Runner execx.Runner
	// Root is the sysfs mount point; defaults to /sys.
	Root string
	// Modprobe is the modprobe binary; defaults to /sbin/modprobe.
	Modprobe string
	// IsRoot reports whether the process may write to sysfs. When nil the
	// process is assumed to have permission (tests).
	IsRoot func() bool
	// Log receives rollback and progress messages. May be nil.
	Log *slog.Logger
	// Unsupported marks a Binder that cannot operate (non-Linux platforms).
	Unsupported bool
}

// snapshot is the subset of a device's sysfs state the sequence re-reads.
type snapshot struct {
	VID          string
	PID          string
	Serial       string
	Driver       string
	PowerControl string
	DevNum       int
	Status       int
}

// Bind exports dev through usbip-host, following the documented sequence.
func (b *Binder) Bind(ctx context.Context, dev sysfs.Device, opts Options) error {
	if b.Unsupported || b.FS == nil {
		return ErrUnsupported
	}
	if err := b.preflight(ctx); err != nil {
		return err
	}

	// Step 2: re-read and confirm the device has not been swapped underneath us.
	cur, err := b.read(dev.BusID)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrNotFound, dev.BusID)
	}
	if !sameIdentity(dev, cur) {
		return fmt.Errorf("%w: identity changed for %s", ErrNotFound, dev.BusID)
	}
	// Already exported: nothing to do.
	if cur.Driver == driverName && cur.Status == statusExported {
		return nil
	}
	// Step 3: refuse to disturb an attached client unless forced.
	if cur.Status == statusAttached && !opts.Force {
		return ErrInUse
	}

	generation := cur.DevNum
	savedPower := cur.PowerControl

	// Step 4: disable autosuspend.
	if err := b.write(b.powerControl(dev.BusID), "on"); err != nil {
		return fmt.Errorf("usbiphost: set power/control: %w", err)
	}
	// Step 5: allow the busid to be claimed by usbip-host.
	if err := b.write(b.matchBusid(), "add "+dev.BusID); err != nil {
		return b.rollback(dev.BusID, savedPower, fmt.Errorf("usbiphost: match_busid add: %w", err))
	}
	// Step 6a: release the current driver (btusb/xpad/xone).
	if err := b.write(b.driverUnbind(dev.BusID), dev.BusID); err != nil {
		return b.rollback(dev.BusID, savedPower, fmt.Errorf("usbiphost: unbind current driver: %w", err))
	}
	// Step 6b: bind usbip-host.
	if err := b.write(b.usbipHostBind(), dev.BusID); err != nil {
		return b.rollback(dev.BusID, savedPower, fmt.Errorf("usbiphost: bind usbip-host: %w", err))
	}
	// Step 7: verify.
	if err := b.verify(dev.BusID, generation); err != nil {
		return b.rollback(dev.BusID, savedPower, err)
	}
	return nil
}

// Unbind releases dev from usbip-host and lets the original driver claim it
// again. It is a no-op only when the device is gone.
//
// The steps are idempotent: even when the device is no longer on usbip-host
// (for example after a previous Unbind failed between two writes), the
// match_busid claim is always dropped and drivers_probe is always run so a
// device that was left driverless is recovered, and power/control is restored.
func (b *Binder) Unbind(ctx context.Context, dev sysfs.Device, opts Options) error {
	if b.Unsupported || b.FS == nil {
		return ErrUnsupported
	}
	if err := b.preflight(ctx); err != nil {
		return err
	}
	cur, err := b.read(dev.BusID)
	if err != nil {
		return nil
	}
	// Same guard as Bind: do not disturb an attached client unless forced.
	if cur.Status == statusAttached && !opts.Force {
		return ErrInUse
	}

	// Step 1: release usbip-host when it is the current driver.
	if cur.Driver == driverName {
		if err := b.write(b.usbipHostUnbind(), dev.BusID); err != nil {
			return fmt.Errorf("usbiphost: unbind: %w", err)
		}
	}
	// Step 2: always drop the busid claim. The kernel reports ENODEV when the
	// busid was never added, which is not a failure.
	if err := b.write(b.matchBusid(), "del "+dev.BusID); err != nil && !tolerateMissing(err) {
		return fmt.Errorf("usbiphost: match_busid del: %w", err)
	}
	// Step 3: always re-probe so a driverless device gets a driver back.
	if err := b.write(b.driversProbe(), dev.BusID); err != nil {
		return fmt.Errorf("usbiphost: drivers_probe: %w", err)
	}
	// Step 4: restore autosuspend unless it is already at the kernel default.
	if cur.PowerControl != "" && cur.PowerControl != "auto" {
		if err := b.write(b.powerControl(dev.BusID), "auto"); err != nil {
			return fmt.Errorf("usbiphost: restore power/control: %w", err)
		}
	}
	return nil
}

// tolerateMissing reports whether a write failed because the entry did not
// exist, which callers treat as already done. The kernel's match_busid_store
// returns ENODEV (wrapped by os.WriteFile in a *fs.PathError) when asked to
// drop a busid that was never added, so the caller can proceed. EINVAL, which
// the same attribute returns for a malformed command, is tolerated defensively,
// as is a genuinely missing file.
func tolerateMissing(err error) bool {
	return errors.Is(err, syscall.ENODEV) ||
		errors.Is(err, syscall.EINVAL) ||
		errors.Is(err, os.ErrNotExist)
}

// preflight requires root and the usbip-host driver, loading the modules when
// they are missing.
func (b *Binder) preflight(ctx context.Context) error {
	if b.IsRoot != nil && !b.IsRoot() {
		return ErrNotRoot
	}
	if b.FS.Exists(b.usbipHostDir()) {
		return nil
	}
	if b.Runner == nil {
		return ErrModuleMissing
	}
	// modprobe -a loads every named module. Failure is not fatal on its own:
	// the re-check below decides.
	_, _, _ = b.Runner.Run(ctx, b.modprobe(), "-a", "usbip-core", "usbip-host")
	if !b.FS.Exists(b.usbipHostDir()) {
		return ErrModuleMissing
	}
	return nil
}

// verify checks that the driver symlink and usbip_status now report an export,
// and that the device generation (devnum) did not change mid-bind.
func (b *Binder) verify(busid string, generation int) error {
	driver := linkBase(b.FS, b.driverLink(busid))
	if driver != driverName {
		return fmt.Errorf("%w: driver is %q, want %q", ErrVerify, driver, driverName)
	}
	cur, err := b.read(busid)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrVerify, err)
	}
	if cur.Status != statusExported {
		return fmt.Errorf("%w: usbip_status is %d, want %d", ErrVerify, cur.Status, statusExported)
	}
	if generation != 0 && cur.DevNum != generation {
		return fmt.Errorf("%w: generation changed (%d -> %d)", ErrVerify, generation, cur.DevNum)
	}
	return nil
}

// rollback undoes a partially completed bind, best-effort, and wraps cause so
// that both ErrRolledBack and the original error remain matchable.
func (b *Binder) rollback(busid, savedPower string, cause error) error {
	b.warnf("bind failed for %s, rolling back: %v", busid, cause)

	if err := b.write(b.usbipHostUnbind(), busid); err != nil {
		b.warnf("rollback: usbip-host unbind for %s: %v", busid, err)
	}
	if err := b.write(b.matchBusid(), "del "+busid); err != nil && !tolerateMissing(err) {
		b.warnf("rollback: match_busid del for %s: %v", busid, err)
	}
	if err := b.write(b.driversProbe(), busid); err != nil {
		b.warnf("rollback: drivers_probe for %s: %v", busid, err)
	}
	if savedPower != "" {
		if err := b.write(b.powerControl(busid), savedPower); err != nil {
			b.warnf("rollback: restore power/control for %s: %v", busid, err)
		}
	}
	return fmt.Errorf("%w: %w", ErrRolledBack, cause)
}

// read re-reads the device state the sequence depends on.
func (b *Binder) read(busid string) (snapshot, error) {
	dir := b.devDir(busid)
	vid, err := b.readAttr(path.Join(dir, "idVendor"))
	if err != nil || vid == "" {
		return snapshot{}, fmt.Errorf("usbiphost: read idVendor for %s", busid)
	}
	s := snapshot{
		VID:    strings.ToLower(vid),
		PID:    strings.ToLower(mustAttr(b, path.Join(dir, "idProduct"))),
		Serial: mustAttr(b, path.Join(dir, "serial")),
		DevNum: atoi(mustAttr(b, path.Join(dir, "devnum"))),
		Status: atoi(mustAttr(b, path.Join(dir, "usbip_status"))),
	}
	s.PowerControl = mustAttr(b, b.powerControl(busid))
	s.Driver = linkBase(b.FS, b.driverLink(busid))
	return s, nil
}

func (b *Binder) write(name, data string) error {
	b.logf("write %s = %q", name, data)
	if err := b.FS.WriteFile(name, []byte(data)); err != nil {
		return fmt.Errorf("usbiphost: write %s: %w", name, err)
	}
	return nil
}

func (b *Binder) readAttr(name string) (string, error) {
	raw, err := b.FS.ReadFile(name)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(raw)), nil
}

func mustAttr(b *Binder, name string) string {
	v, _ := b.readAttr(name)
	return v
}

func sameIdentity(want sysfs.Device, got snapshot) bool {
	if !strings.EqualFold(want.VID, got.VID) || !strings.EqualFold(want.PID, got.PID) {
		return false
	}
	return want.Serial == got.Serial
}

func (b *Binder) root() string {
	if b.Root == "" {
		return "/sys"
	}
	return b.Root
}

func (b *Binder) modprobe() string {
	if b.Modprobe == "" {
		return "/sbin/modprobe"
	}
	return b.Modprobe
}

func (b *Binder) devDir(busid string) string {
	return path.Join(b.root(), "bus/usb/devices", busid)
}

func (b *Binder) driverLink(busid string) string {
	return path.Join(b.devDir(busid), "driver")
}

func (b *Binder) driverUnbind(busid string) string {
	return path.Join(b.devDir(busid), "driver", "unbind")
}

func (b *Binder) powerControl(busid string) string {
	return path.Join(b.devDir(busid), "power", "control")
}

func (b *Binder) usbipHostDir() string {
	return path.Join(b.root(), "bus/usb/drivers/usbip-host")
}

func (b *Binder) usbipHostBind() string {
	return path.Join(b.usbipHostDir(), "bind")
}

func (b *Binder) usbipHostUnbind() string {
	return path.Join(b.usbipHostDir(), "unbind")
}

func (b *Binder) matchBusid() string {
	return path.Join(b.usbipHostDir(), "match_busid")
}

func (b *Binder) driversProbe() string {
	return path.Join(b.root(), "bus/usb/drivers_probe")
}

func (b *Binder) logf(format string, args ...any) {
	if b.Log != nil {
		b.Log.Debug(fmt.Sprintf(format, args...))
	}
}

func (b *Binder) warnf(format string, args ...any) {
	if b.Log != nil {
		b.Log.Warn(fmt.Sprintf(format, args...))
	}
}

func linkBase(fsys SysFS, name string) string {
	target, err := fsys.ReadLink(name)
	if err != nil || target == "" {
		return ""
	}
	return path.Base(target)
}

func atoi(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}
