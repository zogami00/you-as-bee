package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

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
}

func (b *fakeBinder) Bind(_ context.Context, dev sysfs.Device, _ usbiphost.Options) error {
	b.binds++
	if b.failBind {
		return errors.New("bind failed")
	}
	b.src.setDriver(dev.BusID, "usbip-host", 1)
	return nil
}

func (b *fakeBinder) Unbind(_ context.Context, dev sysfs.Device) error {
	b.unbinds++
	if b.failUnbind {
		return errors.New("unbind failed")
	}
	b.src.setDriver(dev.BusID, "btusb", 0)
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

func btDevice() sysfs.Device {
	return sysfs.Device{BusID: "1-1.2", VID: "0a12", PID: "0001", DevNum: 5, Driver: "btusb"}
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
