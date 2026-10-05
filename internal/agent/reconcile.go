// Package agent reconciles the configured device pins against the USB devices
// that are actually attached, driving the usbip-host bind sequence.
//
// All sysfs writes are owned by a single goroutine (Run); the API-facing
// methods only mutate intent and wake that goroutine. Timers are injectable so
// backoff, the stable-export reset and quarantine can be tested without
// sleeping.
package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/zogami00/you-as-bee/internal/config"
	"github.com/zogami00/you-as-bee/internal/identity"
	"github.com/zogami00/you-as-bee/internal/proto"
	"github.com/zogami00/you-as-bee/internal/sysfs"
	"github.com/zogami00/you-as-bee/internal/usbiphost"
)

// ErrUnknown is returned by the API-facing methods when an id selects no
// configured pin or enumerated device.
var ErrUnknown = errors.New("agent: unknown device")

const (
	baseBackoff      = 1 * time.Second
	maxBackoff       = 60 * time.Second
	jitterFraction   = 0.2
	failureWindow    = 5 * time.Minute
	quarantinePeriod = 10 * time.Minute
	exportedStable   = 2 * time.Minute
	maxFailures      = 5

	driverName    = "usbip-host"
	genericDriver = "usb"
	statusOK      = 1
	statusInUse   = 2
	statusFailed  = 3
)

// Source enumerates the USB devices currently attached to the host.
type Source interface {
	Enumerate() ([]sysfs.Device, error)
}

// Binder binds and unbinds devices from the usbip-host driver.
type Binder interface {
	Bind(ctx context.Context, dev sysfs.Device, opts usbiphost.Options) error
	Unbind(ctx context.Context, dev sysfs.Device, opts usbiphost.Options) error
}

// Event is a change notification carrying the generation (devnum) the sender
// observed. Stale events, referring to a generation that has since been
// re-enumerated, must be ignored.
type Event struct {
	BusID  string
	DevNum int
}

type record struct {
	state           State
	backoff         time.Duration
	nextAttempt     time.Time
	exportedAt      time.Time
	failures        []time.Time
	quarantineUntil time.Time
	gen             int
	// lastErr is the most recent bind/unbind failure message, surfaced through
	// proto.Device.LastError and cleared by a successful action or a reset.
	lastErr string
}

// Reconciler owns all device state and performs every sysfs write.
type Reconciler struct {
	Pins     []config.DeviceConfig
	Source   Source
	Binder   Binder
	Log      *slog.Logger
	Interval time.Duration

	// Now returns the current time; defaults to time.Now.
	Now func() time.Time
	// Rand returns a value in [0,1) used for backoff jitter; defaults to
	// math/rand.Float64.
	Rand func() float64

	// Version, Hostname, Started and UsbipdUp populate proto.Info.
	Version  string
	Hostname string
	Started  time.Time
	UsbipdUp func() bool

	mu sync.Mutex
	// passMu serializes whole reconciliation passes. Run is the only
	// production caller, but ReconcileOnce is exported; without this a second
	// concurrent caller would plan from the same pre-bind state and double-bind.
	// It enforces the single-sysfs-writer invariant; r.mu only protects the
	// in-memory records.
	passMu   sync.Mutex
	explicit map[string]bool
	force    map[string]bool
	rec      map[string]*record
	gens     map[string]int
	snapshot []proto.Device
	bybusid  map[string]string
	subs     map[int]chan proto.Event
	nextSub  int
	wake     chan struct{}
}

// New returns a Reconciler with defaults applied.
func New(pins []config.DeviceConfig, src Source, binder Binder) *Reconciler {
	return &Reconciler{
		Pins:     pins,
		Source:   src,
		Binder:   binder,
		Interval: 5 * time.Second,
		explicit: make(map[string]bool),
		force:    make(map[string]bool),
		rec:      make(map[string]*record),
		gens:     make(map[string]int),
		bybusid:  make(map[string]string),
		subs:     make(map[int]chan proto.Event),
		wake:     make(chan struct{}, 1),
	}
}

func (r *Reconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Reconciler) rand() float64 {
	if r.Rand != nil {
		return r.Rand()
	}
	return rand.Float64()
}

func (r *Reconciler) logf(format string, args ...any) {
	if r.Log != nil {
		r.Log.Debug(fmt.Sprintf(format, args...))
	}
}

func (r *Reconciler) warnf(format string, args ...any) {
	if r.Log != nil {
		r.Log.Warn(fmt.Sprintf(format, args...))
	}
}

func (r *Reconciler) errorf(format string, args ...any) {
	if r.Log != nil {
		r.Log.Error(fmt.Sprintf(format, args...))
	}
}

// Run reconciles until ctx is cancelled. It is the only goroutine that calls
// the Binder.
func (r *Reconciler) Run(ctx context.Context) {
	interval := r.Interval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	if err := r.ReconcileOnce(ctx); err != nil {
		r.logf("reconcile: %v", err)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-r.wake:
		}
		if err := r.ReconcileOnce(ctx); err != nil {
			r.logf("reconcile: %v", err)
		}
	}
}

// plannedAction is a bind or unbind the reconciler has decided to perform. It
// is computed under r.mu and executed while the lock is released, so an API
// status call never blocks behind a sysfs write or a modprobe.
type plannedAction struct {
	pin     string
	dev     sysfs.Device
	force   bool
	recover bool // unbind the current driver before binding
	release bool // not desired: unbind only
	err     error
}

// ReconcileOnce performs a single reconciliation pass. It is idempotent: when
// nothing changed since the previous pass it performs no bind or unbind calls.
//
// Passes are serialized by passMu so two callers can never run the bind
// sequence concurrently; the reconcile goroutine remains the only writer to
// sysfs. The state lock (r.mu) is held only to snapshot the desired state and
// to commit the outcome; the Binder (and therefore sysfs writes and modprobe)
// runs outside it.
func (r *Reconciler) ReconcileOnce(ctx context.Context) error {
	r.passMu.Lock()
	defer r.passMu.Unlock()

	devs, err := r.Source.Enumerate()
	if err != nil {
		return err
	}
	resolved, ambiguities, _ := identity.Resolve(r.Pins, devs)
	for _, a := range ambiguities {
		r.warnf("pin %q is ambiguous: %d devices match it", a.Pin, len(a.Devices))
	}

	r.mu.Lock()
	planNow := r.now()
	for _, d := range devs {
		r.gens[d.BusID] = d.DevNum
	}
	plans := make([]plannedAction, 0, len(r.Pins))
	for _, pin := range r.Pins {
		dev, present := resolved[pin.Name]
		if p, ok := r.planLocked(pin, dev, present, planNow); ok {
			plans = append(plans, p)
		}
	}
	r.mu.Unlock()

	// Execute the slow work without holding the lock.
	for i := range plans {
		p := &plans[i]
		switch {
		case p.release:
			p.err = r.Binder.Unbind(ctx, p.dev, usbiphost.Options{Force: p.force})
		case p.recover:
			if err := r.Binder.Unbind(ctx, p.dev, usbiphost.Options{Force: p.force}); err != nil {
				p.err = err
				continue
			}
			p.err = r.Binder.Bind(ctx, p.dev, usbiphost.Options{Force: p.force})
		default:
			p.err = r.Binder.Bind(ctx, p.dev, usbiphost.Options{Force: p.force})
		}
	}

	// Commit the outcome and rebuild the snapshot under the lock again.
	r.mu.Lock()
	defer r.mu.Unlock()
	commitNow := r.now()
	for i := range plans {
		r.commitLocked(&plans[i], commitNow)
	}
	r.rebuildSnapshotLocked(resolved, commitNow)
	return nil
}

// planLocked updates the in-memory state that needs no Binder call and returns
// the bind/unbind action for a pin, if any. The caller must hold r.mu.
func (r *Reconciler) planLocked(pin config.DeviceConfig, dev sysfs.Device, present bool, now time.Time) (plannedAction, bool) {
	rec := r.recordLocked(pin.Name)
	desired := pin.Mode == config.ModeAlways || r.explicit[pin.Name]

	if !present {
		rec.state = Absent
		rec.exportedAt = time.Time{}
		// A device that is no longer attached has no current failure; do not
		// keep reporting a stale last_error.
		rec.lastErr = ""
		return plannedAction{}, false
	}

	// Quarantine: cleared only by the timer or an explicit reset.
	if rec.state == Quarantined {
		if now.Before(rec.quarantineUntil) {
			return plannedAction{}, false
		}
		rec.state = Present
		rec.failures = nil
		rec.backoff = 0
		rec.nextAttempt = time.Time{}
	}

	// Once a device has stayed exported long enough, forget past failures.
	if rec.state == Exported && !rec.exportedAt.IsZero() && now.Sub(rec.exportedAt) >= exportedStable {
		rec.backoff = 0
		rec.failures = nil
	}

	healthy := dev.Driver == driverName && dev.Status == statusOK
	exported := dev.Driver == driverName && (dev.Status == statusOK || dev.Status == statusInUse)

	// A client is attached: never disturb it unless explicitly forced. Without
	// force, wait until it detaches (status returns to 1) before acting again.
	// A forced pass falls through so the Bind sequence can disturb the client.
	if dev.Status == statusInUse && !r.force[pin.Name] {
		rec.state = Attached
		rec.exportedAt = now
		return plannedAction{}, false
	}

	if desired {
		if healthy && rec.gen == dev.DevNum {
			// The device is already healthy and no action is needed, so a
			// pending force has nothing to act on. Clear it instead of letting
			// it survive and disturb a client that attaches later.
			delete(r.force, pin.Name)
			if rec.state != Exported {
				rec.state = Exported
			}
			if rec.exportedAt.IsZero() {
				rec.exportedAt = now
			}
			return plannedAction{}, false
		}
		if now.Before(rec.nextAttempt) {
			rec.state = Backoff
			return plannedAction{}, false
		}
		// Recovery: a full unbind/rebind is required when a client error was
		// reported, when the device sits on a non-generic wrong driver, or when
		// a previously exported device is no longer healthy.
		//
		// The generic USB device driver "usb" is not a wrong driver: on a real
		// Pi it is the device-level driver for every un-exported device (the
		// function driver, e.g. btusb, binds to the interfaces), so treating it
		// as wrong would force a pointless Unbind and break the first export.
		wasExported := rec.state == Exported
		wrongDriver := dev.Driver != "" && dev.Driver != driverName && dev.Driver != genericDriver
		rec.state = Binding
		return plannedAction{
			pin:     pin.Name,
			dev:     dev,
			force:   r.force[pin.Name],
			recover: dev.Status == statusFailed || wrongDriver || wasExported,
		}, true
	}

	// Not desired: release anything we exported.
	if exported || rec.state == Exported || rec.state == Attached {
		rec.state = Present
		return plannedAction{pin: pin.Name, dev: dev, force: r.force[pin.Name], release: true}, true
	}
	rec.state = Present
	rec.exportedAt = time.Time{}
	return plannedAction{}, false
}

// commitLocked applies the result of a performed action to the device record.
// The caller must hold r.mu.
func (r *Reconciler) commitLocked(p *plannedAction, now time.Time) {
	rec := r.recordLocked(p.pin)
	// A force is one-shot: consume it only when this action actually carried it,
	// not merely because a pass ran, so a force set between plan and commit is
	// not silently dropped unused.
	if p.force {
		delete(r.force, p.pin)
	}
	if p.release {
		if p.err != nil {
			r.warnf("unbind %s failed: %v", p.dev.BusID, p.err)
			r.failLocked(p.pin, rec, now, p.err)
			return
		}
		rec.gen = 0
		rec.state = Present
		rec.exportedAt = time.Time{}
		rec.lastErr = ""
		return
	}

	if p.err != nil {
		r.warnf("bind %s failed: %v", p.dev.BusID, p.err)
		r.failLocked(p.pin, rec, now, p.err)
		return
	}
	rec.gen = p.dev.DevNum
	rec.exportedAt = now
	rec.state = Exported
	rec.lastErr = ""
}

// failLocked records a bind/reset failure, grows the backoff, and trips the
// quarantine circuit breaker when failures stack up inside the window. The
// caller must hold r.mu.
func (r *Reconciler) failLocked(pin string, rec *record, now time.Time, err error) {
	if err != nil {
		// Store only a short, path-free reason: LastError is exposed through
		// the API, SSE and the UI, and must not leak sysfs internals the way
		// backendError deliberately avoids. The full error is logged above.
		rec.lastErr = safeReason(err.Error())
	}
	rec.failures = append(rec.failures, now)
	cut := now.Add(-failureWindow)
	kept := rec.failures[:0]
	for _, t := range rec.failures {
		if !t.Before(cut) {
			kept = append(kept, t)
		}
	}
	rec.failures = kept

	if len(rec.failures) > maxFailures {
		rec.state = Quarantined
		rec.quarantineUntil = now.Add(quarantinePeriod)
		rec.nextAttempt = rec.quarantineUntil
		r.errorf("pin %q quarantined for %s after %d failures", pin, quarantinePeriod, len(rec.failures))
		return
	}

	if rec.backoff == 0 {
		rec.backoff = baseBackoff
	} else {
		rec.backoff *= 2
		if rec.backoff > maxBackoff {
			rec.backoff = maxBackoff
		}
	}
	jitter := 1 + (r.rand()-0.5)*2*jitterFraction
	delay := time.Duration(float64(rec.backoff) * jitter)
	// Apply the ceiling after jitter so the real delay never exceeds the cap.
	if delay > maxBackoff {
		delay = maxBackoff
	}
	rec.nextAttempt = now.Add(delay)
	rec.state = Backoff
	r.warnf("pin %q failed, retrying in %s", pin, delay)
}

// safeReason reduces an internal bind/unbind error to a short reason that is
// safe to expose through proto.Device.LastError (and therefore /v1/devices,
// SSE and the UI). A message that names a filesystem path is replaced with a
// stable generic reason, because such paths name sysfs internals; the full
// error is still written to the log. A short, path-free message is preserved so
// operators keep the useful part.
func safeReason(msg string) string {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return "operation failed"
	}
	// Collapse control characters so a multi-line error cannot smuggle a
	// second line into the field.
	msg = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		return r
	}, msg)
	for _, field := range strings.Fields(msg) {
		if strings.HasPrefix(field, "/") {
			return "operation failed"
		}
	}
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return msg
}

// rebuildSnapshotLocked republishes the device snapshot and emits events for
// every presence or state change. The caller must hold r.mu.
func (r *Reconciler) rebuildSnapshotLocked(resolved map[string]sysfs.Device, now time.Time) {
	previous := make(map[string]proto.Device, len(r.snapshot))
	for _, pd := range r.snapshot {
		previous[pd.Pin] = pd
	}

	snapshot := make([]proto.Device, 0, len(r.Pins))
	bybusid := make(map[string]string, len(r.Pins))

	for _, pin := range r.Pins {
		rec := r.recordLocked(pin.Name)
		dev, present := resolved[pin.Name]
		pd := toProto(pin, rec.state, rec.lastErr, dev, present)
		snapshot = append(snapshot, pd)
		if present {
			bybusid[dev.BusID] = pin.Name
		}

		prev, seen := previous[pin.Name]
		switch {
		case !seen && pd.Present:
			r.publishLocked(proto.Event{Type: proto.EventDeviceAdded, Device: pd, At: now})
		case seen && prev.Present != pd.Present:
			typ := proto.EventDeviceAdded
			if !pd.Present {
				typ = proto.EventDeviceRemoved
			}
			r.publishLocked(proto.Event{Type: typ, Device: pd, At: now})
		}
		if seen && prev.State != pd.State {
			r.publishLocked(proto.Event{Type: proto.EventStateChanged, Device: pd, At: now})
		}
		// A changed error message without a state change is still a meaningful
		// update for the UI, so raise state_changed for it too. The Windows
		// supervisor ignores state_changed (see client.Manager.HandleEvent).
		if seen && prev.State == pd.State && prev.LastError != pd.LastError {
			r.publishLocked(proto.Event{Type: proto.EventStateChanged, Device: pd, At: now})
		}
	}

	r.snapshot = snapshot
	r.bybusid = bybusid
}

func (r *Reconciler) recordLocked(name string) *record {
	rec := r.rec[name]
	if rec == nil {
		rec = &record{}
		r.rec[name] = rec
	}
	return rec
}

func (r *Reconciler) signal() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// Export marks a pin for export. A force is one-shot: it is consumed by the
// next reconciliation pass that acts on the pin, then cleared, so a later
// attach is refused unless forced again.
func (r *Reconciler) Export(_ context.Context, id string, force bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	name, ok := r.resolvePinLocked(id)
	if !ok {
		return ErrUnknown
	}
	r.explicit[name] = true
	if force {
		r.force[name] = true
	}
	r.signal()
	return nil
}

// Unexport clears the explicit export intent for a pin.
func (r *Reconciler) Unexport(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	name, ok := r.resolvePinLocked(id)
	if !ok {
		return ErrUnknown
	}
	delete(r.explicit, name)
	delete(r.force, name)
	r.signal()
	return nil
}

// Reset clears backoff, failures and quarantine for a pin.
func (r *Reconciler) Reset(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	name, ok := r.resolvePinLocked(id)
	if !ok {
		return ErrUnknown
	}
	rec := r.recordLocked(name)
	rec.state = Present
	rec.backoff = 0
	rec.nextAttempt = time.Time{}
	rec.failures = nil
	rec.quarantineUntil = time.Time{}
	rec.lastErr = ""
	r.signal()
	return nil
}

// Notify records an observed device generation. It returns false when the
// event is stale, meaning the device has since been re-enumerated with a
// different devnum and acting on the event would be wrong.
func (r *Reconciler) Notify(ev Event) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if current, ok := r.gens[ev.BusID]; ok && current != ev.DevNum {
		return false
	}
	return true
}

// State returns the current lifecycle state of a pin (for diagnostics and
// tests).
func (r *Reconciler) State(pin string) State {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.recordLocked(pin).state
}

// Info implements the management API backend.
func (r *Reconciler) Info(_ context.Context) proto.Info {
	up := false
	if r.UsbipdUp != nil {
		up = r.UsbipdUp()
	}
	var uptime int64
	if !r.Started.IsZero() {
		uptime = int64(r.now().Sub(r.Started).Seconds())
	}
	return proto.Info{
		Version:   r.Version,
		Hostname:  r.Hostname,
		UptimeSec: uptime,
		UsbipdUp:  up,
	}
}

// Devices implements the management API backend.
func (r *Reconciler) Devices(_ context.Context) []proto.Device {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]proto.Device(nil), r.snapshot...)
}

// Device implements the management API backend.
func (r *Reconciler) Device(_ context.Context, id string) (proto.Device, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	name, ok := r.resolvePinLocked(id)
	if !ok {
		return proto.Device{}, false
	}
	for _, pd := range r.snapshot {
		if pd.Pin == name {
			return pd, true
		}
	}
	return proto.Device{}, false
}

// Subscribe implements the management API backend, returning an event channel
// and a cancel function.
func (r *Reconciler) Subscribe(_ context.Context) (<-chan proto.Event, func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id := r.nextSub
	r.nextSub++
	ch := make(chan proto.Event, 16)
	r.subs[id] = ch
	cancel := func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		delete(r.subs, id)
	}
	return ch, cancel
}

func (r *Reconciler) publishLocked(ev proto.Event) {
	for _, ch := range r.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// resolvePinLocked maps an API id to a configured pin name. Only configured
// pins (by name) and the busids of devices the agent enumerated resolve. The
// caller must hold r.mu.
func (r *Reconciler) resolvePinLocked(id string) (string, bool) {
	for _, p := range r.Pins {
		if p.Name == id {
			return p.Name, true
		}
	}
	if name, ok := r.bybusid[id]; ok {
		return name, true
	}
	return "", false
}

func toProto(pin config.DeviceConfig, st State, lastErr string, dev sysfs.Device, present bool) proto.Device {
	mode := pin.Mode
	if mode == "" {
		mode = config.ModeOnDemand
	}
	pd := proto.Device{
		Pin:       pin.Name,
		Mode:      mode,
		Present:   present,
		State:     stateToProto(st),
		LastError: lastErr,
	}
	if present {
		pd.BusID = dev.BusID
		pd.VID = dev.VID
		pd.PID = dev.PID
		pd.Serial = dev.Serial
		pd.Product = dev.Product
		pd.Driver = dev.Driver
	}
	return pd
}

func stateToProto(st State) string {
	switch st {
	case Absent:
		return proto.StateAbsent
	case Present, Binding:
		return proto.StateUnexported
	case Exported:
		return proto.StateExported
	case Attached:
		return proto.StateInUse
	default:
		return proto.StateError
	}
}
