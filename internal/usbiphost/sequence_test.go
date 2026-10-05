package usbiphost

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"syscall"
	"testing"

	"github.com/zogami00/you-as-bee/internal/sysfs"
)

// writeOp records one WriteFile call.
type writeOp struct {
	Name string
	Data string
}

// fakeSysFS is an in-memory SysFS that records every write.
type fakeSysFS struct {
	files   map[string]string
	links   map[string]string
	dirs    map[string]bool
	writes  []writeOp
	onWrite func(name, data string) error
}

func newFakeSysFS() *fakeSysFS {
	return &fakeSysFS{
		files: make(map[string]string),
		links: make(map[string]string),
		dirs:  make(map[string]bool),
	}
}

func (f *fakeSysFS) addFile(name, content string) { f.files[name] = content }
func (f *fakeSysFS) addLink(name, target string)  { f.links[name] = target }

func (f *fakeSysFS) ReadFile(name string) ([]byte, error) {
	if v, ok := f.files[name]; ok {
		return []byte(v), nil
	}
	return nil, errors.New("no such file: " + name)
}

func (f *fakeSysFS) WriteFile(name string, data []byte) error {
	f.writes = append(f.writes, writeOp{Name: name, Data: string(data)})
	if f.onWrite != nil {
		return f.onWrite(name, string(data))
	}
	f.files[name] = string(data)
	return nil
}

func (f *fakeSysFS) ReadLink(name string) (string, error) {
	if v, ok := f.links[name]; ok {
		return v, nil
	}
	return "", errors.New("not a symlink: " + name)
}

func (f *fakeSysFS) Exists(name string) bool {
	if _, ok := f.files[name]; ok {
		return true
	}
	if _, ok := f.links[name]; ok {
		return true
	}
	if f.dirs[name] {
		return true
	}
	prefix := name + "/"
	for k := range f.files {
		if strings.HasPrefix(k, prefix) {
			return true
		}
	}
	for k := range f.links {
		if strings.HasPrefix(k, prefix) {
			return true
		}
	}
	return false
}

const (
	testRoot   = "/sys"
	testBusID  = "1-1.2"
	testDevDir = testRoot + "/bus/usb/devices/" + testBusID
	testHost   = testRoot + "/bus/usb/drivers/usbip-host"
	testMatch  = testHost + "/match_busid"
	testBind   = testHost + "/bind"
	testUnbind = testHost + "/unbind"
	testProbe  = testRoot + "/bus/usb/drivers_probe"
	testPower  = testDevDir + "/power/control"
	testUnbnd  = testDevDir + "/driver/unbind"
	testDrvLn  = testDevDir + "/driver"
)

// newBoundTestFS builds a tree for a Bluetooth dongle currently on the generic
// "us" device driver (btusb binds to the interface, not the device).
func newBoundTestFS() *fakeSysFS {
	f := newFakeSysFS()
	f.addFile(testDevDir+"/idVendor", "0a12")
	f.addFile(testDevDir+"/idProduct", "0001")
	f.addFile(testDevDir+"/devnum", "5")
	f.addFile(testDevDir+"/usbip_status", "0")
	f.addFile(testPower, "auto")
	f.addFile(testMatch, "")
	f.addLink(testDrvLn, testRoot+"/bus/usb/drivers/us")
	return f
}

func testDevice() sysfs.Device {
	return sysfs.Device{BusID: testBusID, VID: "0a12", PID: "0001"}
}

func newBinder(f *fakeSysFS) *Binder {
	return &Binder{FS: f, Root: testRoot, IsRoot: func() bool { return true }}
}

func assertWrites(t *testing.T, f *fakeSysFS, want []writeOp) {
	t.Helper()
	if len(f.writes) != len(want) {
		t.Fatalf("got %d writes %v, want %d %v", len(f.writes), f.writes, len(want), want)
	}
	for i := range want {
		if f.writes[i] != want[i] {
			t.Errorf("write %d = %+v, want %+v", i, f.writes[i], want[i])
		}
	}
}

func TestBindSuccessSequence(t *testing.T) {
	f := newBoundTestFS()
	f.onWrite = func(name, _ string) error {
		if name == testBind {
			f.links[testDrvLn] = testRoot + "/bus/usb/drivers/usbip-host"
			f.files[testDevDir+"/usbip_status"] = "1"
		}
		return nil
	}

	if err := newBinder(f).Bind(context.Background(), testDevice(), Options{}); err != nil {
		t.Fatalf("Bind: %v", err)
	}

	assertWrites(t, f, []writeOp{
		{testPower, "on"},
		{testMatch, "add " + testBusID},
		{testUnbnd, testBusID},
		{testBind, testBusID},
	})
}

func TestBindRollbackOnVerifyFailure(t *testing.T) {
	f := newBoundTestFS() // the bind write does not change the driver => verify fails

	err := newBinder(f).Bind(context.Background(), testDevice(), Options{})
	if !errors.Is(err, ErrVerify) {
		t.Errorf("error = %v, want ErrVerify", err)
	}
	if !errors.Is(err, ErrRolledBack) {
		t.Errorf("error = %v, want ErrRolledBack", err)
	}

	assertWrites(t, f, []writeOp{
		{testPower, "on"},
		{testMatch, "add " + testBusID},
		{testUnbnd, testBusID},
		{testBind, testBusID},
		{testUnbind, testBusID},
		{testMatch, "del " + testBusID},
		{testProbe, testBusID},
		{testPower, "auto"},
	})
}

// The four tests below fail a bind step after the power write and assert the
// exact ordered rollback. Only the verify failure was covered before.
func TestBindPowerFailureDoesNotRollBack(t *testing.T) {
	f := newBoundTestFS()
	f.onWrite = func(name, _ string) error {
		if name == testPower {
			return errors.New("write: operation not permitted")
		}
		return nil
	}

	err := newBinder(f).Bind(context.Background(), testDevice(), Options{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if errors.Is(err, ErrRolledBack) {
		t.Errorf("error = %v; nothing changed, must not report a rollback", err)
	}
	assertWrites(t, f, []writeOp{{testPower, "on"}})
}

func TestBindMatchBusidAddFailureRollsBack(t *testing.T) {
	f := newBoundTestFS()
	f.onWrite = func(name, data string) error {
		if name == testMatch && strings.HasPrefix(data, "add") {
			return errors.New("write: operation not permitted")
		}
		return nil
	}

	err := newBinder(f).Bind(context.Background(), testDevice(), Options{})
	if !errors.Is(err, ErrRolledBack) {
		t.Errorf("error = %v, want ErrRolledBack", err)
	}
	assertWrites(t, f, []writeOp{
		{testPower, "on"},
		{testMatch, "add " + testBusID},
		{testUnbind, testBusID},
		{testMatch, "del " + testBusID},
		{testProbe, testBusID},
		{testPower, "auto"},
	})
}

func TestBindDriverUnbindFailureRollsBack(t *testing.T) {
	f := newBoundTestFS()
	f.onWrite = func(name, _ string) error {
		if name == testUnbnd {
			return errors.New("write: no such device")
		}
		return nil
	}

	err := newBinder(f).Bind(context.Background(), testDevice(), Options{})
	if !errors.Is(err, ErrRolledBack) {
		t.Errorf("error = %v, want ErrRolledBack", err)
	}
	assertWrites(t, f, []writeOp{
		{testPower, "on"},
		{testMatch, "add " + testBusID},
		{testUnbnd, testBusID},
		{testUnbind, testBusID},
		{testMatch, "del " + testBusID},
		{testProbe, testBusID},
		{testPower, "auto"},
	})
}

func TestBindUsbipHostBindFailureRollsBack(t *testing.T) {
	f := newBoundTestFS()
	f.onWrite = func(name, _ string) error {
		if name == testBind {
			return errors.New("write: no such device")
		}
		return nil
	}

	err := newBinder(f).Bind(context.Background(), testDevice(), Options{})
	if !errors.Is(err, ErrRolledBack) {
		t.Errorf("error = %v, want ErrRolledBack", err)
	}
	assertWrites(t, f, []writeOp{
		{testPower, "on"},
		{testMatch, "add " + testBusID},
		{testUnbnd, testBusID},
		{testBind, testBusID},
		{testUnbind, testBusID},
		{testMatch, "del " + testBusID},
		{testProbe, testBusID},
		{testPower, "auto"},
	})
}

func TestBindAlreadyExportedIsIdempotent(t *testing.T) {
	f := newBoundTestFS()
	f.links[testDrvLn] = testRoot + "/bus/usb/drivers/usbip-host"
	f.files[testDevDir+"/usbip_status"] = "1"

	if err := newBinder(f).Bind(context.Background(), testDevice(), Options{}); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if len(f.writes) != 0 {
		t.Errorf("writes = %v, want none for an already exported device", f.writes)
	}
}

func TestBindRefusesAttachedWithoutForce(t *testing.T) {
	f := newBoundTestFS()
	f.links[testDrvLn] = testRoot + "/bus/usb/drivers/usbip-host"
	f.files[testDevDir+"/usbip_status"] = "2"

	err := newBinder(f).Bind(context.Background(), testDevice(), Options{})
	if !errors.Is(err, ErrInUse) {
		t.Errorf("error = %v, want ErrInUse", err)
	}
	if len(f.writes) != 0 {
		t.Errorf("writes = %v, want none", f.writes)
	}
}

func TestBindForceAllowsAttached(t *testing.T) {
	f := newBoundTestFS()
	f.links[testDrvLn] = testRoot + "/bus/usb/drivers/usbip-host"
	f.files[testDevDir+"/usbip_status"] = "2"
	f.onWrite = func(name, _ string) error {
		if name == testBind {
			f.files[testDevDir+"/usbip_status"] = "1"
		}
		return nil
	}

	if err := newBinder(f).Bind(context.Background(), testDevice(), Options{Force: true}); err != nil {
		t.Fatalf("Bind with Force: %v", err)
	}
}

func TestBindIdentityMismatch(t *testing.T) {
	f := newBoundTestFS()
	other := sysfs.Device{BusID: testBusID, VID: "045e", PID: "02e6"}

	err := newBinder(f).Bind(context.Background(), other, Options{})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
	if len(f.writes) != 0 {
		t.Errorf("writes = %v, want none", f.writes)
	}
}

func TestUnbindSequence(t *testing.T) {
	f := newBoundTestFS()
	f.links[testDrvLn] = testRoot + "/bus/usb/drivers/usbip-host"
	f.files[testDevDir+"/usbip_status"] = "1"
	f.files[testPower] = "on" // bind set autosuspend off; Unbind must restore it

	if err := newBinder(f).Unbind(context.Background(), testDevice(), Options{}); err != nil {
		t.Fatalf("Unbind: %v", err)
	}
	assertWrites(t, f, []writeOp{
		{testUnbind, testBusID},
		{testMatch, "del " + testBusID},
		{testProbe, testBusID},
		{testPower, "auto"},
	})
}

// TestUnbindCompletesStepsWhenDriverAbsent covers a device that is no longer on
// usbip-host (for example after an interrupted Unbind): the remaining steps
// must still run so it cannot be left driverless.
func TestUnbindCompletesStepsWhenDriverAbsent(t *testing.T) {
	f := newBoundTestFS() // on "us", status 0

	if err := newBinder(f).Unbind(context.Background(), testDevice(), Options{}); err != nil {
		t.Fatalf("Unbind: %v", err)
	}
	assertWrites(t, f, []writeOp{
		{testMatch, "del " + testBusID},
		{testProbe, testBusID},
	})
}

func TestUnbindRefusesAttachedWithoutForce(t *testing.T) {
	f := newBoundTestFS()
	f.links[testDrvLn] = testRoot + "/bus/usb/drivers/usbip-host"
	f.files[testDevDir+"/usbip_status"] = "2"

	err := newBinder(f).Unbind(context.Background(), testDevice(), Options{})
	if !errors.Is(err, ErrInUse) {
		t.Errorf("error = %v, want ErrInUse", err)
	}
	if len(f.writes) != 0 {
		t.Errorf("writes = %v, want none", f.writes)
	}
}

func TestUnbindForceAllowsAttached(t *testing.T) {
	f := newBoundTestFS()
	f.links[testDrvLn] = testRoot + "/bus/usb/drivers/usbip-host"
	f.files[testDevDir+"/usbip_status"] = "2"

	if err := newBinder(f).Unbind(context.Background(), testDevice(), Options{Force: true}); err != nil {
		t.Fatalf("Unbind with Force: %v", err)
	}
}

// TestUnbindFailedFirstWriteThenRetry: the usbip-host/unbind write fails, so
// the device is untouched. The retry must complete the whole sequence.
func TestUnbindFailedFirstWriteThenRetry(t *testing.T) {
	f := newBoundTestFS()
	f.links[testDrvLn] = testRoot + "/bus/usb/drivers/usbip-host"
	f.files[testDevDir+"/usbip_status"] = "1"
	failUnbind := true
	f.onWrite = func(name, _ string) error {
		if name == testUnbind && failUnbind {
			return errors.New("write: input/output error")
		}
		if name == testUnbind {
			delete(f.links, testDrvLn)
			f.files[testDevDir+"/usbip_status"] = "0"
		}
		return nil
	}

	b := newBinder(f)
	if err := b.Unbind(context.Background(), testDevice(), Options{}); err == nil {
		t.Fatal("first Unbind: expected error, got nil")
	}
	f.writes = nil
	failUnbind = false

	if err := b.Unbind(context.Background(), testDevice(), Options{}); err != nil {
		t.Fatalf("retry Unbind: %v", err)
	}
	assertWrites(t, f, []writeOp{
		{testUnbind, testBusID},
		{testMatch, "del " + testBusID},
		{testProbe, testBusID},
	})
}

// TestUnbindRetryAfterMidSequenceFailure: usbip-host/unbind succeeds but
// match_busid del fails, leaving the device off usbip-host. Because the old
// code treated a driverless device as a no-op, the retry must still drop the
// busid claim and re-probe.
func TestUnbindRetryAfterMidSequenceFailure(t *testing.T) {
	f := newBoundTestFS()
	f.links[testDrvLn] = testRoot + "/bus/usb/drivers/usbip-host"
	f.files[testDevDir+"/usbip_status"] = "1"
	failMatch := true
	f.onWrite = func(name, _ string) error {
		if name == testUnbind {
			delete(f.links, testDrvLn) // device is now driverless
			f.files[testDevDir+"/usbip_status"] = "0"
		}
		if name == testMatch && failMatch {
			return errors.New("write: invalid argument")
		}
		return nil
	}

	b := newBinder(f)
	if err := b.Unbind(context.Background(), testDevice(), Options{}); err == nil {
		t.Fatal("first Unbind: expected error, got nil")
	}
	f.writes = nil
	failMatch = false

	if err := b.Unbind(context.Background(), testDevice(), Options{}); err != nil {
		t.Fatalf("retry Unbind: %v", err)
	}
	// The retry must not try to unbind usbip-host again (the driver is gone),
	// but it must still drop the claim and cause a re-probe.
	assertWrites(t, f, []writeOp{
		{testMatch, "del " + testBusID},
		{testProbe, testBusID},
	})
}

// TestTolerateMissingErrnos pins the errnos that count as "already done". The
// kernel's match_busid_store returns ENODEV (not EINVAL) when asked to drop a
// busid that was never added; os.WriteFile wraps it in a *fs.PathError. EINVAL
// is tolerated defensively, and a genuinely missing file too.
func TestTolerateMissingErrnos(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"ENODEV", &fs.PathError{Op: "write", Path: testMatch, Err: syscall.ENODEV}, true},
		{"EINVAL", &fs.PathError{Op: "write", Path: testMatch, Err: syscall.EINVAL}, true},
		{"ENOENT", &fs.PathError{Op: "write", Path: testMatch, Err: syscall.ENOENT}, true},
		{"EACCES", &fs.PathError{Op: "write", Path: testMatch, Err: syscall.EACCES}, false},
		{"plain error", errors.New("boom"), false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tolerateMissing(tc.err); got != tc.want {
				t.Errorf("tolerateMissing(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestUnbindToleratesENODEVFromMatchBusid: a real kernel returns ENODEV when
// match_busid is asked to drop a busid that was never added. Unbind must treat
// that as success and still re-probe.
func TestUnbindToleratesENODEVFromMatchBusid(t *testing.T) {
	f := newBoundTestFS()
	f.links[testDrvLn] = testRoot + "/bus/usb/drivers/usbip-host"
	f.files[testDevDir+"/usbip_status"] = "1"
	f.onWrite = func(name, data string) error {
		if name == testMatch && strings.HasPrefix(data, "del") {
			return &fs.PathError{Op: "write", Path: name, Err: syscall.ENODEV}
		}
		return nil
	}

	b := newBinder(f)
	if err := b.Unbind(context.Background(), testDevice(), Options{}); err != nil {
		t.Fatalf("Unbind with ENODEV on match_busid: %v", err)
	}
	assertWrites(t, f, []writeOp{
		{testUnbind, testBusID},
		{testMatch, "del " + testBusID},
		{testProbe, testBusID},
	})
}

// TestUnbindToleratesEINVALFromMatchBusid: EINVAL is tolerated defensively.
func TestUnbindToleratesEINVALFromMatchBusid(t *testing.T) {
	f := newBoundTestFS()
	f.links[testDrvLn] = testRoot + "/bus/usb/drivers/usbip-host"
	f.files[testDevDir+"/usbip_status"] = "1"
	f.onWrite = func(name, data string) error {
		if name == testMatch && strings.HasPrefix(data, "del") {
			return &fs.PathError{Op: "write", Path: name, Err: syscall.EINVAL}
		}
		return nil
	}

	if err := newBinder(f).Unbind(context.Background(), testDevice(), Options{}); err != nil {
		t.Fatalf("Unbind with EINVAL on match_busid: %v", err)
	}
}

// TestUnbindRetryAfterProbeFailureToleratesENODEV covers the partial-failure
// retry: the first pass removes usbip-host and the match_busid entry, but
// drivers_probe fails. On the retry the busid is already gone, so the kernel
// returns ENODEV for `del`; that must be tolerated so drivers_probe still runs
// and the driverless device is recovered.
func TestUnbindRetryAfterProbeFailureToleratesENODEV(t *testing.T) {
	f := newBoundTestFS()
	f.links[testDrvLn] = testRoot + "/bus/usb/drivers/usbip-host"
	f.files[testDevDir+"/usbip_status"] = "1"
	probeFails := true
	f.onWrite = func(name, _ string) error {
		switch {
		case name == testUnbind:
			delete(f.links, testDrvLn) // device is now driverless
			f.files[testDevDir+"/usbip_status"] = "0"
		case name == testProbe && probeFails:
			return errors.New("write: input/output error")
		}
		return nil
	}

	b := newBinder(f)
	if err := b.Unbind(context.Background(), testDevice(), Options{}); err == nil {
		t.Fatal("first Unbind: expected the probe failure, got nil")
	}
	f.writes = nil
	probeFails = false
	// The busid is already gone, so the kernel now rejects `del` with ENODEV.
	f.onWrite = func(name, _ string) error {
		if name == testMatch {
			return &fs.PathError{Op: "write", Path: name, Err: syscall.ENODEV}
		}
		return nil
	}

	if err := b.Unbind(context.Background(), testDevice(), Options{}); err != nil {
		t.Fatalf("retry Unbind: %v", err)
	}
	assertWrites(t, f, []writeOp{
		{testMatch, "del " + testBusID},
		{testProbe, testBusID},
	})
}

func TestPreflightNotRoot(t *testing.T) {
	f := newBoundTestFS()
	b := newBinder(f)
	b.IsRoot = func() bool { return false }

	if err := b.Bind(context.Background(), testDevice(), Options{}); !errors.Is(err, ErrNotRoot) {
		t.Errorf("error = %v, want ErrNotRoot", err)
	}
}

func TestPreflightModuleMissing(t *testing.T) {
	f := newFakeSysFS() // no usbip-host directory
	b := newBinder(f)
	b.Runner = fixedRunner{err: errors.New("modprobe failed")}

	if err := b.Bind(context.Background(), testDevice(), Options{}); !errors.Is(err, ErrModuleMissing) {
		t.Errorf("error = %v, want ErrModuleMissing", err)
	}
}

func TestPreflightLoadsModule(t *testing.T) {
	f := newFakeSysFS()
	b := newBinder(f)
	runner := &loadingRunner{fs: f, path: testMatch}
	b.Runner = runner

	// The runner fakes modprobe by creating the match_busid file, which makes
	// the usbip-host directory exist. The device itself is still missing, so
	// Bind proceeds past preflight and fails with ErrNotFound, proving the
	// modprobe path ran.
	err := b.Bind(context.Background(), testDevice(), Options{})
	if errors.Is(err, ErrModuleMissing) {
		t.Errorf("error = %v, want preflight to have succeeded", err)
	}
	if err == nil {
		t.Fatal("expected a bind error for a missing device")
	}
	if runner.calls != 1 {
		t.Fatalf("modprobe called %d times, want 1", runner.calls)
	}
	if runner.path0 != "/sbin/modprobe" {
		t.Errorf("modprobe path = %q, want /sbin/modprobe", runner.path0)
	}
	wantArgs := "-a,usbip-core,usbip-host"
	if got := strings.Join(runner.args0, ","); got != wantArgs {
		t.Errorf("modprobe args = %q, want %q", got, wantArgs)
	}
}

// fixedRunner always returns the configured error.
type fixedRunner struct{ err error }

func (r fixedRunner) Run(_ context.Context, _ string, _ ...string) (string, string, error) {
	return "", "", r.err
}

// loadingRunner simulates modprobe creating the usbip-host directory and
// records how it was invoked.
type loadingRunner struct {
	fs    *fakeSysFS
	path  string
	calls int
	path0 string
	args0 []string
}

func (r *loadingRunner) Run(_ context.Context, name string, args ...string) (string, string, error) {
	r.calls++
	if r.path0 == "" {
		r.path0 = name
		r.args0 = append([]string(nil), args...)
	}
	r.fs.files[r.path] = ""
	return "", "", nil
}
