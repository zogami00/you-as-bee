package agent

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/zogami00/you-as-bee/internal/config"
	"github.com/zogami00/you-as-bee/internal/proto"
	"github.com/zogami00/you-as-bee/internal/sysfs"
	"github.com/zogami00/you-as-bee/internal/usbiphost"
)

// --- fakes ---

type fakeSource struct {
	mu   sync.Mutex
	devs []sysfs.Device
}

func newFakeSource(devs ...sysfs.Device) *fakeSource { return &fakeSource{devs: devs} }

func (s *fakeSource) Enumerate() ([]sysfs.Device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]sysfs.Device(nil), s.devs...), nil
}

func (s *fakeSource) set(devs ...sysfs.Device) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.devs = append([]sysfs.Device(nil), devs...)
}

func (s *fakeSource) setDriver(busid, driver string, status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.devs {
		if s.devs[i].BusID == busid {
			s.devs[i].Driver = driver
			s.devs[i].Status = status
		}
	}
}

type fakeBinder struct {
	src        *fakeSource
	binds      int
	unbinds    int
	failBind   bool
	failUnbind bool
	// bindErr, when set, is returned by Bind instead of the generic failure.
	bindErr error

	// bindEntered is closed when a bind starts and bindRelease gates its
	// completion, so a test can hold a bind in flight.
	bindEntered chan struct{}
	bindRelease chan struct{}
	bindOnce    sync.Once
}

func (b *fakeBinder) Bind(_ context.Context, dev sysfs.Device, _ usbiphost.Options) error {
	b.binds++
	if b.bindEntered != nil {
		b.bindOnce.Do(func() { close(b.bindEntered) })
	}
	if b.bindRelease != nil {
		<-b.bindRelease
	}
	if b.failBind {
		if b.bindErr != nil {
			return b.bindErr
		}
		return errors.New("bind failed")
	}
	b.src.setDriver(dev.BusID, "usbip-host", 1)
	return nil
}

func (b *fakeBinder) Unbind(_ context.Context, dev sysfs.Device, _ usbiphost.Options) error {
	b.unbinds++
	if b.failUnbind {
		return errors.New("unbind failed")
	}
	// A released device falls back to the generic USB device driver; the
	// function driver (here btusb) binds to the interface, not the device.
	b.src.setDriver(dev.BusID, "usb", 0)
	return nil
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func (c *fakeClock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

// --- helpers ---

// btDevice models a real un-exported Bluetooth dongle: the device-level driver
// is the generic "usb", while the function driver btusb is bound to the
// interface. Treating "usb" as a wrong driver used to force a pointless Unbind
// and break the first export.
func btDevice() sysfs.Device {
	return sysfs.Device{
		BusID: "1-1.2", VID: "0a12", PID: "0001", DevNum: 5, Driver: "usb",
		Interfaces: []sysfs.Iface{{BusID: "1-1.2:1.0", Driver: "btusb"}},
	}
}

func testPins() []config.DeviceConfig {
	return []config.DeviceConfig{{Name: "bt", VID: "0a12", PID: "0001", Mode: config.ModeAlways}}
}

func newTestReconciler(t *testing.T, src *fakeSource, binder *fakeBinder) (*Reconciler, *fakeClock) {
	t.Helper()
	clock := newFakeClock()
	r := New(testPins(), src, binder)
	r.Now = clock.Now
	r.Rand = func() float64 { return 0.5 }
	return r, clock
}

// --- tests ---

func TestReconcileIsIdempotent(t *testing.T) {
	src := newFakeSource(btDevice())
	binder := &fakeBinder{src: src}
	r, _ := newTestReconciler(t, src, binder)

	if err := r.ReconcileOnce(context.Background()); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	if r.State("bt") != Exported {
		t.Fatalf("state = %s, want exported", r.State("bt"))
	}
	binds, unbinds := binder.binds, binder.unbinds

	if err := r.ReconcileOnce(context.Background()); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if binder.binds != binds || binder.unbinds != unbinds {
		t.Errorf("second pass made calls: binds %d->%d, unbinds %d->%d",
			binds, binder.binds, unbinds, binder.unbinds)
	}
}

func TestReconcileRebindsWrongDriver(t *testing.T) {
	src := newFakeSource(btDevice())
	binder := &fakeBinder{src: src}
	r, _ := newTestReconciler(t, src, binder)

	if err := r.ReconcileOnce(context.Background()); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	// Simulate something else stealing the device back.
	src.setDriver("1-1.2", "btusb", 0)
	before := binder.binds

	if err := r.ReconcileOnce(context.Background()); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if binder.binds != before+1 {
		t.Errorf("binds = %d, want %d after a wrong-driver recovery", binder.binds, before+1)
	}
	if r.State("bt") != Exported {
		t.Errorf("state = %s, want exported", r.State("bt"))
	}
}

func TestBackoffGrowsOnRepeatedFailure(t *testing.T) {
	src := newFakeSource(btDevice())
	binder := &fakeBinder{src: src, failBind: true}
	r, clock := newTestReconciler(t, src, binder)

	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
	var got []time.Duration
	for range want {
		if err := r.ReconcileOnce(context.Background()); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
		r.mu.Lock()
		got = append(got, r.rec["bt"].backoff)
		next := r.rec["bt"].nextAttempt
		r.mu.Unlock()
		clock.set(next)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("backoff[%d] = %s, want %s", i, got[i], want[i])
		}
	}
}

func TestBackoffResetsAfterStableExport(t *testing.T) {
	src := newFakeSource(btDevice())
	binder := &fakeBinder{src: src, failBind: true}
	r, clock := newTestReconciler(t, src, binder)

	// Build up a backoff of 4s.
	for i := 0; i < 3; i++ {
		if err := r.ReconcileOnce(context.Background()); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
		r.mu.Lock()
		next := r.rec["bt"].nextAttempt
		r.mu.Unlock()
		clock.set(next)
	}
	r.mu.Lock()
	if r.rec["bt"].backoff != 4*time.Second {
		t.Fatalf("backoff = %s, want 4s", r.rec["bt"].backoff)
	}
	r.mu.Unlock()

	// Succeed, then stay exported for two minutes.
	binder.failBind = false
	clock.set(clock.Now().Add(4 * time.Second))
	if err := r.ReconcileOnce(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if r.State("bt") != Exported {
		t.Fatalf("state = %s, want exported", r.State("bt"))
	}
	clock.advance(exportedStable)

	// Fail again: the backoff must restart at 1s.
	src.setDriver("1-1.2", "btusb", 0)
	binder.failBind = true
	if err := r.ReconcileOnce(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	r.mu.Lock()
	got := r.rec["bt"].backoff
	r.mu.Unlock()
	if got != time.Second {
		t.Errorf("backoff after a stable export = %s, want 1s", got)
	}
}

func TestQuarantineTripsAndTimerClears(t *testing.T) {
	src := newFakeSource(btDevice())
	binder := &fakeBinder{src: src, failBind: true}
	r, clock := newTestReconciler(t, src, binder)

	// maxFailures+1 failures inside the window trip the breaker.
	for i := 0; i < maxFailures+1; i++ {
		if err := r.ReconcileOnce(context.Background()); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
		r.mu.Lock()
		next := r.rec["bt"].nextAttempt
		r.mu.Unlock()
		clock.set(next)
	}
	if r.State("bt") != Quarantined {
		t.Fatalf("state = %s, want quarantined", r.State("bt"))
	}
	r.mu.Lock()
	until := r.rec["bt"].quarantineUntil
	r.mu.Unlock()

	// Still quarantined just before the timer expires.
	clock.set(until.Add(-time.Second))
	if err := r.ReconcileOnce(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if r.State("bt") != Quarantined {
		t.Fatalf("state = %s, want still quarantined", r.State("bt"))
	}

	// The timer clears it.
	clock.set(until.Add(2 * time.Second))
	if err := r.ReconcileOnce(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if r.State("bt") == Quarantined {
		t.Errorf("state = %s, want timer to have cleared the quarantine", r.State("bt"))
	}
}

func TestResetClearsQuarantine(t *testing.T) {
	src := newFakeSource(btDevice())
	binder := &fakeBinder{src: src, failBind: true}
	r, clock := newTestReconciler(t, src, binder)

	for i := 0; i < maxFailures+1; i++ {
		if err := r.ReconcileOnce(context.Background()); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
		r.mu.Lock()
		next := r.rec["bt"].nextAttempt
		r.mu.Unlock()
		clock.set(next)
	}
	if r.State("bt") != Quarantined {
		t.Fatalf("state = %s, want quarantined", r.State("bt"))
	}

	if err := r.Reset(context.Background(), "bt"); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if r.State("bt") == Quarantined {
		t.Errorf("state = %s, want reset to have cleared the quarantine", r.State("bt"))
	}
}

func TestStaleGenerationEventsIgnored(t *testing.T) {
	src := newFakeSource(btDevice())
	binder := &fakeBinder{src: src}
	r, _ := newTestReconciler(t, src, binder)

	if err := r.ReconcileOnce(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !r.Notify(Event{BusID: "1-1.2", DevNum: 5}) {
		t.Fatal("Notify(current generation) = false, want true")
	}

	// The device re-enumerates with a new devnum.
	src.mu.Lock()
	src.devs[0].DevNum = 6
	src.mu.Unlock()
	if err := r.ReconcileOnce(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	if r.Notify(Event{BusID: "1-1.2", DevNum: 5}) {
		t.Error("Notify(stale generation) = true, want false")
	}
	if !r.Notify(Event{BusID: "1-1.2", DevNum: 6}) {
		t.Error("Notify(current generation) = false, want true")
	}
}

func TestAttachedDeviceIsNotDisturbed(t *testing.T) {
	dev := sysfs.Device{BusID: "1-1.2", VID: "0a12", PID: "0001", DevNum: 5, Driver: "usbip-host", Status: 2}
	src := newFakeSource(dev)
	binder := &fakeBinder{src: src}
	r, _ := newTestReconciler(t, src, binder)

	if err := r.ReconcileOnce(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if r.State("bt") != Attached {
		t.Errorf("state = %s, want attached", r.State("bt"))
	}
	if binder.binds != 0 || binder.unbinds != 0 {
		t.Errorf("binds/unbinds = %d/%d, want 0/0 for an attached client", binder.binds, binder.unbinds)
	}
}

func TestAbsentThenPresent(t *testing.T) {
	src := newFakeSource()
	binder := &fakeBinder{src: src}
	r, _ := newTestReconciler(t, src, binder)

	if err := r.ReconcileOnce(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if r.State("bt") != Absent {
		t.Fatalf("state = %s, want absent", r.State("bt"))
	}

	src.set(btDevice())
	if err := r.ReconcileOnce(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if r.State("bt") != Exported {
		t.Fatalf("state = %s, want exported", r.State("bt"))
	}

	src.set()
	if err := r.ReconcileOnce(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if r.State("bt") != Absent {
		t.Errorf("state = %s, want absent again", r.State("bt"))
	}
}

func TestSubscribeReceivesStateChange(t *testing.T) {
	src := newFakeSource()
	binder := &fakeBinder{src: src}
	r, _ := newTestReconciler(t, src, binder)

	ctx := context.Background()
	ch, cancel := r.Subscribe(ctx)
	defer cancel()

	if err := r.ReconcileOnce(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	src.set(btDevice())
	if err := r.ReconcileOnce(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	select {
	case ev := <-ch:
		if ev.Device.Pin != "bt" {
			t.Errorf("event device = %+v, want pin bt", ev.Device)
		}
		if ev.Type != proto.EventDeviceAdded && ev.Type != proto.EventStateChanged {
			t.Errorf("event type = %q", ev.Type)
		}
	default:
		t.Fatal("no event received")
	}
}

// TestJitterStaysWithinCap checks that the backoff ceiling is applied after
// jitter (the bug made a 60s cap reach 72s).
func TestJitterStaysWithinCap(t *testing.T) {
	r := New(nil, newFakeSource(), &fakeBinder{})
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	r.Rand = func() float64 { return 1 } // maximum upward jitter
	rec := &record{backoff: maxBackoff}
	r.mu.Lock()
	r.failLocked("bt", rec, now, nil)
	r.mu.Unlock()
	if got := rec.nextAttempt.Sub(now); got > maxBackoff {
		t.Errorf("delay with max jitter = %s, want <= %s", got, maxBackoff)
	}

	r.Rand = func() float64 { return 0 } // maximum downward jitter
	rec = &record{backoff: maxBackoff}
	r.mu.Lock()
	r.failLocked("bt", rec, now, nil)
	r.mu.Unlock()
	minDelay := time.Duration(float64(maxBackoff) * (1 - jitterFraction))
	if got := rec.nextAttempt.Sub(now); got < minDelay {
		t.Errorf("delay with min jitter = %s, want >= %s", got, minDelay)
	}
}

// TestForceIsOneShot verifies that a forced export is consumed by the pass that
// acts on it instead of persisting until Unexport.
func TestForceIsOneShot(t *testing.T) {
	src := newFakeSource(btDevice())
	binder := &fakeBinder{src: src}
	r, _ := newTestReconciler(t, src, binder)
	ctx := context.Background()

	if err := r.Export(ctx, "bt", true); err != nil {
		t.Fatalf("Export: %v", err)
	}
	if err := r.ReconcileOnce(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	r.mu.Lock()
	_, still := r.force["bt"]
	r.mu.Unlock()
	if still {
		t.Error("force was not consumed by the pass that acted on it")
	}
}

// TestDevicesCompletesWhileBindInFlight is the regression for holding r.mu
// across Binder.Bind: the API must remain responsive during a bind.
func TestDevicesCompletesWhileBindInFlight(t *testing.T) {
	src := newFakeSource(btDevice())
	binder := &fakeBinder{
		src:         src,
		bindEntered: make(chan struct{}),
		bindRelease: make(chan struct{}),
	}
	r, _ := newTestReconciler(t, src, binder)

	done := make(chan error, 1)
	go func() { done <- r.ReconcileOnce(context.Background()) }()

	select {
	case <-binder.bindEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("bind never started")
	}

	devs := make(chan int, 1)
	go func() { devs <- len(r.Devices(context.Background())) }()
	select {
	case <-devs:
	case <-time.After(2 * time.Second):
		close(binder.bindRelease)
		t.Fatal("Devices blocked while a bind was in flight")
	}

	close(binder.bindRelease)
	if err := <-done; err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}
}

// TestReconcileRetriesFailedUnbind drives a failed Unbind from the reconciler
// and checks that a later pass retries it.
func TestReconcileRetriesFailedUnbind(t *testing.T) {
	dev := sysfs.Device{BusID: "1-1.2", VID: "0a12", PID: "0001", DevNum: 5, Driver: "usbip-host", Status: 1}
	src := newFakeSource(dev)
	binder := &fakeBinder{src: src, failUnbind: true}
	clock := newFakeClock()
	pins := []config.DeviceConfig{{Name: "bt", VID: "0a12", PID: "0001", Mode: config.ModeOnDemand}}
	r := New(pins, src, binder)
	r.Now = clock.Now
	r.Rand = func() float64 { return 0.5 }

	ctx := context.Background()
	if err := r.ReconcileOnce(ctx); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	if r.State("bt") != Backoff {
		t.Fatalf("state = %s, want backoff after a failed unbind", r.State("bt"))
	}
	if binder.unbinds != 1 {
		t.Fatalf("unbinds = %d, want 1", binder.unbinds)
	}

	binder.failUnbind = false
	r.mu.Lock()
	next := r.rec["bt"].nextAttempt
	r.mu.Unlock()
	clock.set(next)

	if err := r.ReconcileOnce(ctx); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if binder.unbinds != 2 {
		t.Errorf("unbinds = %d, want 2 (the failed unbind must be retried)", binder.unbinds)
	}
	if r.State("bt") != Present {
		t.Errorf("state = %s, want present after the retry", r.State("bt"))
	}
}

// TestFirstExportWithGenericDriverDoesNotUnbind: on a real Pi an un-exported
// device sits on the generic "usb" device driver (btusb is bound to the
// interfaces). The first export must go straight to Bind with no prior Unbind.
func TestFirstExportWithGenericDriverDoesNotUnbind(t *testing.T) {
	src := newFakeSource(btDevice()) // Driver "usb", Status 0
	binder := &fakeBinder{src: src}
	r, _ := newTestReconciler(t, src, binder)

	if err := r.ReconcileOnce(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if binder.unbinds != 0 {
		t.Errorf("unbinds = %d, want 0: the generic usb driver is not a wrong driver", binder.unbinds)
	}
	if binder.binds != 1 {
		t.Errorf("binds = %d, want 1", binder.binds)
	}
	if r.State("bt") != Exported {
		t.Errorf("state = %s, want exported", r.State("bt"))
	}
}

// TestRecoversFromUsbipStatusFailed: usbip_status == 3 is a genuine recovery
// trigger even when the device-level driver is the generic "usb", so the pass
// must Unbind before it Binds.
func TestRecoversFromUsbipStatusFailed(t *testing.T) {
	dev := btDevice()
	dev.Status = statusFailed
	src := newFakeSource(dev)
	binder := &fakeBinder{src: src}
	r, _ := newTestReconciler(t, src, binder)

	if err := r.ReconcileOnce(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if binder.unbinds != 1 || binder.binds != 1 {
		t.Errorf("binds/unbinds = %d/%d, want 1/1 for usbip_status == 3", binder.binds, binder.unbinds)
	}
	if r.State("bt") != Exported {
		t.Errorf("state = %s, want exported", r.State("bt"))
	}
}

// TestForceActsOnAttachedDevice: force=true must produce an action for a
// status-2 (attached) device, matching what docs/api.md promises.
func TestForceActsOnAttachedDevice(t *testing.T) {
	dev := sysfs.Device{BusID: "1-1.2", VID: "0a12", PID: "0001", DevNum: 5, Driver: "usbip-host", Status: 2}
	src := newFakeSource(dev)
	binder := &fakeBinder{src: src}
	r, _ := newTestReconciler(t, src, binder)
	ctx := context.Background()

	if err := r.Export(ctx, "bt", true); err != nil {
		t.Fatalf("Export: %v", err)
	}
	if err := r.ReconcileOnce(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if binder.binds+binder.unbinds == 0 {
		t.Fatal("force on an attached device must produce a bind/unbind action")
	}
}

// TestForceConsumedOnlyWhenUsed: a pending force must survive a pass whose
// action did not carry it, instead of being silently dropped.
func TestForceConsumedOnlyWhenUsed(t *testing.T) {
	r := New(nil, newFakeSource(), &fakeBinder{})
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r.force["bt"] = true

	r.mu.Lock()
	r.commitLocked(&plannedAction{pin: "bt", dev: sysfs.Device{BusID: "1-1.2", DevNum: 5}}, now)
	_, still := r.force["bt"]
	r.mu.Unlock()

	if !still {
		t.Error("an action that did not carry force must not consume the pending force")
	}
}

// TestForceOnHealthyDeviceDoesNotDisturbLaterAttach: a force issued against a
// device that is already healthy and exported has no action to run, so it must
// be cleared. Otherwise it stays pending and a client that attaches normally
// afterwards is kicked by an unnecessary Unbind+Bind.
func TestForceOnHealthyDeviceDoesNotDisturbLaterAttach(t *testing.T) {
	dev := sysfs.Device{BusID: "1-1.2", VID: "0a12", PID: "0001", DevNum: 5, Driver: "usbip-host", Status: 1}
	src := newFakeSource(dev)
	binder := &fakeBinder{src: src}
	r, _ := newTestReconciler(t, src, binder)
	ctx := context.Background()

	// Establish the exported generation so the healthy branch is reached.
	if err := r.ReconcileOnce(ctx); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	binds, unbinds := binder.binds, binder.unbinds

	// A forced export on an already-healthy device runs no action.
	if err := r.Export(ctx, "bt", true); err != nil {
		t.Fatalf("Export: %v", err)
	}
	if err := r.ReconcileOnce(ctx); err != nil {
		t.Fatalf("forced reconcile: %v", err)
	}
	if binder.binds != binds || binder.unbinds != unbinds {
		t.Fatalf("forced pass on a healthy device acted: binds %d->%d, unbinds %d->%d",
			binds, binder.binds, unbinds, binder.unbinds)
	}
	r.mu.Lock()
	_, still := r.force["bt"]
	r.mu.Unlock()
	if still {
		t.Fatal("force on a healthy device was not cleared")
	}

	// A client now attaches normally; the stale force must not disturb it.
	src.setDriver("1-1.2", "usbip-host", 2)
	if err := r.ReconcileOnce(ctx); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if binder.binds != binds || binder.unbinds != unbinds {
		t.Errorf("a normal client attach was disturbed: binds %d->%d, unbinds %d->%d",
			binds, binder.binds, unbinds, binder.unbinds)
	}
	if r.State("bt") != Attached {
		t.Errorf("state = %s, want attached", r.State("bt"))
	}
}

// TestReconcileLogMessagesAreFormatted guards against passing a printf template
// plus args straight to slog, which renders the template literally and turns a
// stray arg into !BADKEY.
func TestReconcileLogMessagesAreFormatted(t *testing.T) {
	src := newFakeSource(btDevice())
	binder := &fakeBinder{src: src, failBind: true}
	r, clock := newTestReconciler(t, src, binder)
	var buf bytes.Buffer
	r.Log = slog.New(slog.NewTextHandler(&buf, nil))

	ctx := context.Background()
	for i := 0; i < maxFailures+1; i++ {
		if err := r.ReconcileOnce(ctx); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
		r.mu.Lock()
		next := r.rec["bt"].nextAttempt
		r.mu.Unlock()
		clock.set(next)
	}

	out := buf.String()
	if strings.Contains(out, "!BADKEY") {
		t.Errorf("log output contains !BADKEY (printf args were treated as key/value pairs):\n%s", out)
	}
	if strings.Contains(out, "%!") || strings.Contains(out, "%q") || strings.Contains(out, "%s") {
		t.Errorf("log output contains an unexpanded format verb:\n%s", out)
	}
	if !strings.Contains(out, "failed, retrying in") {
		t.Errorf("retry message is not fully formatted:\n%s", out)
	}
	if !strings.Contains(out, "quarantined for") {
		t.Errorf("quarantine message is not fully formatted:\n%s", out)
	}
}

// TestConcurrentReconcileIsSerialized: a second concurrent pass must not plan
// from the same pre-bind state and double-bind the device.
func TestConcurrentReconcileIsSerialized(t *testing.T) {
	src := newFakeSource(btDevice())
	binder := &fakeBinder{
		src:         src,
		bindEntered: make(chan struct{}),
		bindRelease: make(chan struct{}),
	}
	r, _ := newTestReconciler(t, src, binder)
	ctx := context.Background()

	first := make(chan error, 1)
	go func() { first <- r.ReconcileOnce(ctx) }()
	select {
	case <-binder.bindEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("first bind never started")
	}

	second := make(chan error, 1)
	go func() { second <- r.ReconcileOnce(ctx) }()
	// Give the second pass time to reach passMu before the first completes.
	time.Sleep(50 * time.Millisecond)

	close(binder.bindRelease)
	if err := <-first; err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if err := <-second; err != nil {
		t.Fatalf("second pass: %v", err)
	}

	if binder.binds != 1 {
		t.Errorf("binds = %d, want 1: concurrent passes double-bound the device", binder.binds)
	}
}

// TestLastErrorSetAndClearedOnSuccess: a failed bind records its error on the
// device and a later successful bind clears it.
func TestLastErrorSetAndClearedOnSuccess(t *testing.T) {
	src := newFakeSource(btDevice())
	binder := &fakeBinder{src: src, failBind: true, bindErr: errors.New("usbip-host bind: permission denied")}
	r, clock := newTestReconciler(t, src, binder)
	ctx := context.Background()

	if err := r.ReconcileOnce(ctx); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	devs := r.Devices(ctx)
	if len(devs) != 1 || devs[0].LastError == "" {
		t.Fatalf("LastError not set after a failed bind: %+v", devs)
	}

	binder.failBind = false
	r.mu.Lock()
	next := r.rec["bt"].nextAttempt
	r.mu.Unlock()
	clock.set(next)
	if err := r.ReconcileOnce(ctx); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if r.State("bt") != Exported {
		t.Fatalf("state = %s, want exported", r.State("bt"))
	}
	if got := r.Devices(ctx)[0].LastError; got != "" {
		t.Errorf("LastError = %q after a successful bind, want empty", got)
	}
}

// TestResetClearsLastError: an explicit reset clears the recorded failure.
func TestResetClearsLastError(t *testing.T) {
	src := newFakeSource(btDevice())
	binder := &fakeBinder{src: src, failBind: true, bindErr: errors.New("boom")}
	r, _ := newTestReconciler(t, src, binder)
	ctx := context.Background()

	if err := r.ReconcileOnce(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if r.Devices(ctx)[0].LastError == "" {
		t.Fatal("LastError not set")
	}
	if err := r.Reset(ctx, "bt"); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	r.mu.Lock()
	got := r.rec["bt"].lastErr
	r.mu.Unlock()
	if got != "" {
		t.Errorf("record lastErr = %q after reset, want empty", got)
	}
}

// TestLastErrorOnlyChangeRaisesStateChanged: when the state is unchanged but the
// failure message changes, the UI must still be told. The Windows supervisor
// ignores state_changed, so this cannot disturb it.
func TestLastErrorOnlyChangeRaisesStateChanged(t *testing.T) {
	src := newFakeSource(btDevice())
	binder := &fakeBinder{src: src, failBind: true, bindErr: errors.New("first failure")}
	r, clock := newTestReconciler(t, src, binder)
	ctx := context.Background()

	if err := r.ReconcileOnce(ctx); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	if got := r.Devices(ctx)[0].LastError; got != "first failure" {
		t.Fatalf("LastError = %q, want first failure", got)
	}

	ch, cancel := r.Subscribe(ctx)
	defer cancel()

	binder.bindErr = errors.New("second failure")
	r.mu.Lock()
	next := r.rec["bt"].nextAttempt
	r.mu.Unlock()
	clock.set(next)
	if err := r.ReconcileOnce(ctx); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}

	select {
	case ev := <-ch:
		if ev.Type != proto.EventStateChanged {
			t.Fatalf("event type = %q, want state_changed", ev.Type)
		}
		if ev.Device.LastError != "second failure" {
			t.Errorf("event device LastError = %q, want second failure", ev.Device.LastError)
		}
	case <-time.After(time.Second):
		t.Fatal("no state_changed event was raised for a LastError change")
	}
}

// TestLastErrorRedactsFilesystemPaths: LastError is exposed through the API,
// SSE and the UI, so a sysfs path in the underlying error must not survive.
func TestLastErrorRedactsFilesystemPaths(t *testing.T) {
	src := newFakeSource(btDevice())
	binder := &fakeBinder{
		src:      src,
		failBind: true,
		bindErr:  errors.New("usbiphost: write /sys/bus/usb/drivers/usbip-host/bind: permission denied"),
	}
	r, _ := newTestReconciler(t, src, binder)
	ctx := context.Background()

	if err := r.ReconcileOnce(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	got := r.Devices(ctx)[0].LastError
	if got == "" {
		t.Fatal("LastError empty, want a short safe reason")
	}
	if strings.Contains(got, "/") {
		t.Errorf("LastError leaked a filesystem path: %q", got)
	}
}

// TestSafeReasonRedactsPathsAnywhere covers the shapes the old first-field
// heuristic missed: a quoted, parenthesised or '='-delimited path that does not
// begin a whitespace-delimited field.
func TestSafeReasonRedactsPathsAnywhere(t *testing.T) {
	leaky := []string{
		`usbiphost: open "/sys/bus/usb/devices/1-1.2/idVendor": permission denied`,
		`write failed (/sys/bus/usb/drivers/usbip-host/bind)`,
		`path=/sys/bus/usb/devices/1-1.2`,
		`/sys/bus/usb/devices/1-1.2`,
	}
	for _, msg := range leaky {
		if got := safeReason(msg); strings.Contains(got, "/") {
			t.Errorf("safeReason(%q) = %q, leaked a path", msg, got)
		}
	}
}

// TestSafeReasonKeepsShortPathFreeMessages: an ordinary error keeps its useful
// text, and a slash used as a conjunction is not treated as a path.
func TestSafeReasonKeepsShortPathFreeMessages(t *testing.T) {
	for _, msg := range []string{"permission denied", "read/write failed", "and/or"} {
		if got := safeReason(msg); got != msg {
			t.Errorf("safeReason(%q) = %q, want the message preserved", msg, got)
		}
	}
}

// TestSafeReasonTruncatesOnRuneBoundary: the 200-byte cap must not split a
// multi-byte rune.
func TestSafeReasonTruncatesOnRuneBoundary(t *testing.T) {
	msg := strings.Repeat("\u20ac", 100) // 300 bytes, so a 200-byte cut is mid-rune
	got := safeReason(msg)
	if !utf8.ValidString(got) {
		t.Fatalf("safeReason produced invalid UTF-8 (len %d)", len(got))
	}
	if len(got) > 200 {
		t.Fatalf("safeReason len = %d, want <= 200", len(got))
	}
	if len(got) == 0 {
		t.Fatal("safeReason returned empty")
	}
}

// TestAbsentDeviceClearsLastError: an unplugged device must not keep reporting
// a stale failure.
func TestAbsentDeviceClearsLastError(t *testing.T) {
	src := newFakeSource(btDevice())
	binder := &fakeBinder{src: src, failBind: true, bindErr: errors.New("boom")}
	r, _ := newTestReconciler(t, src, binder)
	ctx := context.Background()

	if err := r.ReconcileOnce(ctx); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	if r.Devices(ctx)[0].LastError == "" {
		t.Fatal("LastError not set after a failed bind")
	}

	src.set() // the device is gone
	if err := r.ReconcileOnce(ctx); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if got := r.Devices(ctx)[0].LastError; got != "" {
		t.Errorf("LastError = %q after the device went absent, want empty", got)
	}
	if st := r.Devices(ctx)[0].State; st != proto.StateAbsent {
		t.Errorf("state = %q, want absent", st)
	}
}
