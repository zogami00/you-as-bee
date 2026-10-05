package usbiphost

import (
	"context"
	"errors"
	"strings"
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

// newBoundTestFS builds a tree for a Bluetooth dongle currently on btusb.
func newBoundTestFS() *fakeSysFS {
	f := newFakeSysFS()
	f.addFile(testDevDir+"/idVendor", "0a12")
	f.addFile(testDevDir+"/idProduct", "0001")
	f.addFile(testDevDir+"/devnum", "5")
	f.addFile(testDevDir+"/usbip_status", "0")
	f.addFile(testPower, "auto")
	f.addFile(testMatch, "")
	f.addLink(testDrvLn, testRoot+"/bus/usb/drivers/btusb")
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

	if err := newBinder(f).Unbind(context.Background(), testDevice()); err != nil {
		t.Fatalf("Unbind: %v", err)
	}
	assertWrites(t, f, []writeOp{
		{testUnbind, testBusID},
		{testMatch, "del " + testBusID},
		{testProbe, testBusID},
	})
}

func TestUnbindNoopWhenUnexported(t *testing.T) {
	f := newBoundTestFS() // on btusb, status 0

	if err := newBinder(f).Unbind(context.Background(), testDevice()); err != nil {
		t.Fatalf("Unbind: %v", err)
	}
	if len(f.writes) != 0 {
		t.Errorf("writes = %v, want none", f.writes)
	}
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
	b.Runner = &loadingRunner{fs: f, path: testMatch}

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
}

// fixedRunner always returns the configured error.
type fixedRunner struct{ err error }

func (r fixedRunner) Run(_ context.Context, _ string, _ ...string) (string, string, error) {
	return "", "", r.err
}

// loadingRunner simulates modprobe creating the usbip-host directory.
type loadingRunner struct {
	fs   *fakeSysFS
	path string
}

func (r *loadingRunner) Run(_ context.Context, _ string, _ ...string) (string, string, error) {
	r.fs.files[r.path] = ""
	return "", "", nil
}
