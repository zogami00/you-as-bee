package client

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zogami00/you-as-bee/internal/config"
	"github.com/zogami00/you-as-bee/internal/proto"
	"github.com/zogami00/you-as-bee/internal/usbipwin"
)

// ---- fake agent ------------------------------------------------------------

type fakeAgent struct {
	mu       sync.Mutex
	devices  []proto.Device
	healthy  bool
	exports  []string
	requests int
}

func newFakeAgent(devices ...proto.Device) (*httptest.Server, *fakeAgent) {
	fa := &fakeAgent{devices: devices, healthy: true}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		fa.mu.Lock()
		ok := fa.healthy
		fa.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /v1/devices", func(w http.ResponseWriter, _ *http.Request) {
		fa.mu.Lock()
		defer fa.mu.Unlock()
		_ = json.NewEncoder(w).Encode(proto.ListDevicesResponse{Devices: fa.devices})
	})
	mux.HandleFunc("GET /v1/devices/{id}", func(w http.ResponseWriter, r *http.Request) {
		fa.mu.Lock()
		defer fa.mu.Unlock()
		for _, d := range fa.devices {
			if d.Pin == r.PathValue("id") {
				_ = json.NewEncoder(w).Encode(d)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(proto.Error{Code: "not_found", Message: "unknown device"})
	})
	mux.HandleFunc("POST /v1/devices/{id}/export", func(w http.ResponseWriter, r *http.Request) {
		fa.mu.Lock()
		defer fa.mu.Unlock()
		id := r.PathValue("id")
		for i := range fa.devices {
			if fa.devices[i].Pin == id {
				fa.devices[i].State = proto.StateExported
				fa.exports = append(fa.exports, id)
				_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(proto.Error{Code: "not_found", Message: "unknown device"})
	})
	return httptest.NewServer(mux), fa
}

func (fa *fakeAgent) setHealthy(v bool) {
	fa.mu.Lock()
	fa.healthy = v
	fa.mu.Unlock()
}

func (fa *fakeAgent) setDevices(devs ...proto.Device) {
	fa.mu.Lock()
	fa.devices = devs
	fa.mu.Unlock()
}

func (fa *fakeAgent) exportCalls() []string {
	fa.mu.Lock()
	defer fa.mu.Unlock()
	return append([]string(nil), fa.exports...)
}

// ---- fake usbip ------------------------------------------------------------

type attachCall struct{ host, busid string }

type fakeUSBIP struct {
	mu          sync.Mutex
	ports       []usbipwin.PortEntry
	nextPort    int
	attachErr   error
	attachCalls []attachCall
	detachCalls []int
	// reportHost, when set, is written into the port entry instead of the host
	// passed to Attach. It models usbip-win2 printing the resolved IP.
	reportHost string
	// onAttach, when set, runs while Attach is in flight (before the port is
	// recorded) so a test can interleave a Pause.
	onAttach func()
}

func (f *fakeUSBIP) Attach(_ context.Context, host, busid string) error {
	f.mu.Lock()
	hook := f.onAttach
	f.mu.Unlock()
	if hook != nil {
		hook()
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.attachCalls = append(f.attachCalls, attachCall{host, busid})
	if f.attachErr != nil {
		return f.attachErr
	}
	reported := host
	if f.reportHost != "" {
		reported = f.reportHost
	}
	f.ports = append(f.ports, usbipwin.PortEntry{
		Port: f.nextPort, Host: reported, RemotePort: 3240, BusID: busid, VID: "045e", PID: "02ea",
	})
	f.nextPort++
	return nil
}

func (f *fakeUSBIP) Detach(_ context.Context, port int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.detachCalls = append(f.detachCalls, port)
	kept := f.ports[:0]
	for _, p := range f.ports {
		if p.Port != port {
			kept = append(kept, p)
		}
	}
	f.ports = kept
	return nil
}

func (f *fakeUSBIP) Port(_ context.Context) ([]usbipwin.PortEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]usbipwin.PortEntry(nil), f.ports...), nil
}

func (f *fakeUSBIP) ListRemote(context.Context, string) ([]usbipwin.RemoteDevice, error) {
	return nil, nil
}

func (f *fakeUSBIP) setPorts(ports ...usbipwin.PortEntry) {
	f.mu.Lock()
	f.ports = ports
	f.mu.Unlock()
}

func (f *fakeUSBIP) attachCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.attachCalls)
}

func (f *fakeUSBIP) detachCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.detachCalls)
}

func (f *fakeUSBIP) attachedBusIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.attachCalls))
	for _, c := range f.attachCalls {
		out = append(out, c.busid)
	}
	return out
}

// ---- helpers ---------------------------------------------------------------

func testConfig(t *testing.T, ts *httptest.Server, auto ...config.AutoAttach) config.ClientConfig {
	t.Helper()
	u, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	host, portStr, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatalf("split host: %v", err)
	}
	port, _ := strconv.Atoi(portStr)
	return config.ClientConfig{
		SchemaVersion: 1,
		Servers: []config.ServerConfig{{
			Name: "pi", Host: host, APIPort: port, Token: strings.Repeat("a", 64),
		}},
		AutoAttach:     auto,
		Reconnect:      config.ReconnectConfig{Initial: config.Duration(time.Second), Max: config.Duration(10 * time.Second)},
		CommandTimeout: config.Duration(5 * time.Second),
	}
}

func presentDevice(pin, busid, mode, state string) proto.Device {
	return proto.Device{Pin: pin, BusID: busid, VID: "045e", PID: "02ea", Present: true, Mode: mode, State: state}
}

type harness struct {
	m        *Manager
	usbip    *fakeUSBIP
	agent    *fakeAgent
	ts       *httptest.Server
	now      time.Time
	notifyMu sync.Mutex
	notes    []string
}

func newHarness(t *testing.T, cfg config.ClientConfig) *harness {
	t.Helper()
	h := &harness{usbip: &fakeUSBIP{}, now: time.Unix(1_700_000_000, 0)}
	randHalf := func() float64 { return 0.5 }
	opt := Options{
		Config: cfg,
		USBIP:  h.usbip,
		Now:    func() time.Time { return h.now },
		After:  func(time.Duration) <-chan time.Time { return make(chan time.Time) },
		Rand:   randHalf,
		Notify: func(pin, msg string) {
			h.notifyMu.Lock()
			h.notes = append(h.notes, pin+": "+msg)
			h.notifyMu.Unlock()
		},
		RespectExternalDetach: boolPtr(true),
	}
	m, err := New(opt)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h.m = m
	return h
}

func boolPtr(b bool) *bool { return &b }

func (h *harness) pinState(t *testing.T, pin string) *pinState {
	t.Helper()
	h.m.mu.Lock()
	defer h.m.mu.Unlock()
	ps := h.m.pins[pinKey(config.AutoAttach{Server: "pi", Device: pin})]
	if ps == nil {
		t.Fatalf("no pin state for %q", pin)
	}
	return ps
}

func (h *harness) pinByServer(t *testing.T, server, device string) *pinState {
	t.Helper()
	h.m.mu.Lock()
	defer h.m.mu.Unlock()
	ps := h.m.pins[pinKey(config.AutoAttach{Server: server, Device: device})]
	if ps == nil {
		t.Fatalf("no pin state for %s/%s", server, device)
	}
	return ps
}

func (h *harness) reconcile(t *testing.T) {
	t.Helper()
	if err := h.m.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
}

// ---- tests -----------------------------------------------------------------

func TestReconcileAttachesOnDemandDevice(t *testing.T) {
	ts, fa := newFakeAgent(presentDevice("xbox", "1-1.4", config.ModeOnDemand, proto.StateUnexported))
	defer ts.Close()
	h := newHarness(t, testConfig(t, ts, config.AutoAttach{Server: "pi", Device: "xbox"}))

	h.reconcile(t)

	if got := fa.exportCalls(); len(got) != 1 || got[0] != "xbox" {
		t.Fatalf("export calls = %v, want [xbox]", got)
	}
	if got := h.usbip.attachedBusIDs(); len(got) != 1 || got[0] != "1-1.4" {
		t.Fatalf("attach busids = %v, want [1-1.4]", got)
	}
	if ps := h.pinState(t, "xbox"); ps.state != StateAttached || ps.port != 0 {
		t.Fatalf("pin state = %+v, want attached port 0", ps)
	}
}

func TestNetworkDownMakesNoAttachCalls(t *testing.T) {
	ts, fa := newFakeAgent(presentDevice("xbox", "1-1.4", config.ModeAlways, proto.StateExported))
	defer ts.Close()
	h := newHarness(t, testConfig(t, ts, config.AutoAttach{Server: "pi", Device: "xbox"}))
	fa.setHealthy(false)

	h.reconcile(t)

	if h.usbip.attachCount() != 0 {
		t.Fatalf("attach called %d times while API down", h.usbip.attachCount())
	}
	if ps := h.pinState(t, "xbox"); ps.state != StateNetworkDown {
		t.Fatalf("state = %q, want %q", ps.state, StateNetworkDown)
	}
}

func TestDeviceAbsentStaysIdle(t *testing.T) {
	dev := presentDevice("xbox", "1-1.4", config.ModeAlways, proto.StateExported)
	dev.Present = false
	ts, _ := newFakeAgent(dev)
	defer ts.Close()
	h := newHarness(t, testConfig(t, ts, config.AutoAttach{Server: "pi", Device: "xbox"}))

	h.reconcile(t)

	if h.usbip.attachCount() != 0 {
		t.Fatalf("attach called for absent device")
	}
	if ps := h.pinState(t, "xbox"); ps.state != StateAbsent {
		t.Fatalf("state = %q, want %q", ps.state, StateAbsent)
	}
}

func TestTransientDropReattachesAfterOutage(t *testing.T) {
	ts, _ := newFakeAgent(presentDevice("xbox", "1-1.4", config.ModeAlways, proto.StateExported))
	defer ts.Close()
	h := newHarness(t, testConfig(t, ts, config.AutoAttach{Server: "pi", Device: "xbox"}))

	h.reconcile(t) // attach
	if h.usbip.attachCount() != 1 {
		t.Fatalf("first attach count = %d", h.usbip.attachCount())
	}

	// Port vanishes and the API/SSE outage is recent.
	h.usbip.setPorts()
	h.m.noteOutage(h.now)
	h.now = h.now.Add(2 * time.Second)
	h.reconcile(t)

	if h.usbip.attachCount() != 2 {
		t.Fatalf("expected a re-attach after outage, count = %d", h.usbip.attachCount())
	}
	if ps := h.pinState(t, "xbox"); ps.state != StateAttached {
		t.Fatalf("state = %q, want attached", ps.state)
	}
}

func TestExternalDetachPausesAndNotifies(t *testing.T) {
	ts, _ := newFakeAgent(presentDevice("xbox", "1-1.4", config.ModeAlways, proto.StateExported))
	defer ts.Close()
	h := newHarness(t, testConfig(t, ts, config.AutoAttach{Server: "pi", Device: "xbox"}))

	h.reconcile(t) // attach
	if h.usbip.attachCount() != 1 {
		t.Fatalf("first attach count = %d", h.usbip.attachCount())
	}

	// Port vanishes with no outage: reset the outage marker.
	h.m.mu.Lock()
	h.m.lastOutage = time.Time{}
	h.m.mu.Unlock()
	h.usbip.setPorts()
	h.now = h.now.Add(2 * time.Second)
	h.reconcile(t)

	if ps := h.pinState(t, "xbox"); !ps.paused || ps.state != StatePaused {
		t.Fatalf("state = %+v, want paused", ps)
	}
	if h.usbip.attachCount() != 1 {
		t.Fatalf("external detach must not re-attach; count = %d", h.usbip.attachCount())
	}
	if len(h.notes) != 1 {
		t.Fatalf("notify notes = %v, want 1", h.notes)
	}
}

func TestBackoffGrowsJittersAndResets(t *testing.T) {
	ts, _ := newFakeAgent(presentDevice("xbox", "1-1.4", config.ModeAlways, proto.StateExported))
	defer ts.Close()
	h := newHarness(t, testConfig(t, ts, config.AutoAttach{Server: "pi", Device: "xbox"}))
	h.usbip.attachErr = context.DeadlineExceeded

	var got []time.Duration
	for i := 0; i < 5; i++ {
		h.reconcile(t)
		ps := h.pinState(t, "xbox")
		got = append(got, ps.backoff)
		if ps.nextAttempt.Sub(h.now) < time.Duration(float64(ps.backoff)*0.8) ||
			ps.nextAttempt.Sub(h.now) > time.Duration(float64(ps.backoff)*1.2) {
			t.Fatalf("iteration %d: nextAttempt delta %s outside jitter bounds of %s",
				i, ps.nextAttempt.Sub(h.now), ps.backoff)
		}
		h.now = ps.nextAttempt
	}

	want := []time.Duration{
		1 * time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 10 * time.Second,
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("backoff[%d] = %s, want %s (series %v)", i, got[i], want[i], got)
		}
	}

	// Now let attach succeed and advance past the stable window.
	h.usbip.attachErr = nil
	h.reconcile(t)
	ps := h.pinState(t, "xbox")
	if ps.state != StateAttached {
		t.Fatalf("state after success = %q", ps.state)
	}
	h.now = ps.attachedSince.Add(61 * time.Second)
	h.reconcile(t)
	if ps := h.pinState(t, "xbox"); ps.backoff != 0 {
		t.Fatalf("backoff after stable attachment = %s, want 0", ps.backoff)
	}
}

func TestPauseAndResume(t *testing.T) {
	ts, _ := newFakeAgent(presentDevice("xbox", "1-1.4", config.ModeAlways, proto.StateExported))
	defer ts.Close()
	h := newHarness(t, testConfig(t, ts, config.AutoAttach{Server: "pi", Device: "xbox"}))
	h.reconcile(t)

	if err := h.m.Pause(context.Background(), "xbox"); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if !h.m.IsPaused("xbox") {
		t.Fatal("want paused")
	}
	if h.usbip.detachCount() != 1 {
		t.Fatalf("detach calls = %d, want 1", h.usbip.detachCount())
	}
	h.reconcile(t)
	if h.usbip.attachCount() != 1 {
		t.Fatalf("paused pin re-attached; count = %d", h.usbip.attachCount())
	}

	if err := h.m.Resume("xbox"); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if h.m.IsPaused("xbox") {
		t.Fatal("want resumed")
	}
	h.reconcile(t)
	if h.usbip.attachCount() != 2 {
		t.Fatalf("attach count after resume = %d, want 2", h.usbip.attachCount())
	}
}

func TestResumeOnDeviceAddedEvent(t *testing.T) {
	ts, _ := newFakeAgent(presentDevice("xbox", "1-1.4", config.ModeAlways, proto.StateExported))
	defer ts.Close()
	h := newHarness(t, testConfig(t, ts, config.AutoAttach{Server: "pi", Device: "xbox"}))
	if err := h.m.Pause(context.Background(), "xbox"); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	h.m.HandleEvent("pi", proto.Event{Type: proto.EventDeviceAdded, Device: proto.Device{Pin: "xbox", BusID: "1-1.4"}})
	if h.m.IsPaused("xbox") {
		t.Fatal("device_added should clear the pause")
	}
}

// M1: a same-busid vhci port belonging to a DIFFERENT server must be left
// alone. Pi bus ids repeat across Pis, so detaching it would tear down a
// working device on the other server.
func TestStalePortForOtherHostIsNotDetached(t *testing.T) {
	ts, _ := newFakeAgent(presentDevice("xbox", "1-1.4", config.ModeAlways, proto.StateExported))
	defer ts.Close()
	h := newHarness(t, testConfig(t, ts, config.AutoAttach{Server: "pi", Device: "xbox"}))
	h.usbip.setPorts(usbipwin.PortEntry{Port: 7, Host: "10.9.9.9", BusID: "1-1.4", VID: "045e", PID: "02ea"})

	h.reconcile(t)

	for _, p := range h.usbip.detachCalls {
		if p == 7 {
			t.Fatalf("foreign-host port 7 was detached; detaches = %v", h.usbip.detachCalls)
		}
	}
	if h.usbip.attachCount() != 1 {
		t.Fatalf("attach count = %d, want 1", h.usbip.attachCount())
	}
	if len(h.usbip.ports) != 2 {
		t.Fatalf("ports = %+v, want the foreign port and the new one", h.usbip.ports)
	}
}

func TestDetachStaleOnlyTouchesMatchingHost(t *testing.T) {
	ts, _ := newFakeAgent(presentDevice("xbox", "1-1.4", config.ModeAlways, proto.StateExported))
	defer ts.Close()
	h := newHarness(t, testConfig(t, ts, config.AutoAttach{Server: "pi", Device: "xbox"}))
	h.usbip.setPorts(
		usbipwin.PortEntry{Port: 7, Host: "127.0.0.1", BusID: "1-1.4"},
		usbipwin.PortEntry{Port: 8, Host: "10.9.9.9", BusID: "1-1.4"},
	)
	h.m.detachStale(context.Background(), map[string]bool{"127.0.0.1": true}, "1-1.4")

	if h.usbip.detachCount() != 1 || h.usbip.detachCalls[0] != 7 {
		t.Fatalf("detach calls = %v, want [7]", h.usbip.detachCalls)
	}
}

func TestAlwaysUsesFreshBusID(t *testing.T) {
	ts, fa := newFakeAgent(presentDevice("xbox", "1-1.4", config.ModeAlways, proto.StateExported))
	defer ts.Close()
	h := newHarness(t, testConfig(t, ts, config.AutoAttach{Server: "pi", Device: "xbox"}))
	h.reconcile(t)

	// Re-plug moves the device to a new bus id and drops the old port.
	fa.setDevices(presentDevice("xbox", "1-2.1", config.ModeAlways, proto.StateExported))
	h.usbip.setPorts()
	h.m.mu.Lock()
	h.m.lastOutage = h.now // treat as transient
	h.m.mu.Unlock()
	h.now = h.now.Add(time.Second)
	h.reconcile(t)

	busids := h.usbip.attachedBusIDs()
	if len(busids) != 2 || busids[1] != "1-2.1" {
		t.Fatalf("attach busids = %v, want second to be 1-2.1", busids)
	}
}

func TestConfirmFailureCountsAsBackoff(t *testing.T) {
	ts, _ := newFakeAgent(presentDevice("xbox", "1-1.4", config.ModeAlways, proto.StateExported))
	defer ts.Close()
	h := newHarness(t, testConfig(t, ts, config.AutoAttach{Server: "pi", Device: "xbox"}))
	// Attach exits zero but Port never shows the device.
	h.usbip.attachErr = nil
	h.m.opt.USBIP = &noConfirmUSBIP{inner: h.usbip}
	h.reconcile(t)
	if ps := h.pinState(t, "xbox"); ps.state != StateBackoff || ps.backoff == 0 {
		t.Fatalf("state = %+v, want backoff with non-zero backoff", ps)
	}
}

// noConfirmUSBIP reports no ports even after a successful attach.
type noConfirmUSBIP struct{ inner *fakeUSBIP }

func (n *noConfirmUSBIP) Attach(ctx context.Context, host, busid string) error {
	return n.inner.Attach(ctx, host, busid)
}
func (n *noConfirmUSBIP) Detach(ctx context.Context, port int) error {
	return n.inner.Detach(ctx, port)
}
func (n *noConfirmUSBIP) Port(context.Context) ([]usbipwin.PortEntry, error) {
	return nil, nil
}
func (n *noConfirmUSBIP) ListRemote(context.Context, string) ([]usbipwin.RemoteDevice, error) {
	return nil, nil
}

// twoServerConfig builds a config with two named servers that share one fake
// agent, so pin identity can be exercised by (server, device).
func twoServerConfig(t *testing.T, ts *httptest.Server, pairs ...config.AutoAttach) config.ClientConfig {
	t.Helper()
	u, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	host, portStr, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatalf("split host: %v", err)
	}
	port, _ := strconv.Atoi(portStr)
	return config.ClientConfig{
		SchemaVersion: 1,
		Servers: []config.ServerConfig{
			{Name: "pia", Host: host, APIPort: port, Token: strings.Repeat("a", 64)},
			{Name: "pib", Host: host, APIPort: port, Token: strings.Repeat("b", 64)},
		},
		AutoAttach:     pairs,
		Reconnect:      config.ReconnectConfig{Initial: config.Duration(time.Second), Max: config.Duration(10 * time.Second)},
		CommandTimeout: config.Duration(5 * time.Second),
	}
}

// M2: an event from one server must not touch a same-named pin on another.
func TestHandleEventScopedToServer(t *testing.T) {
	ts, _ := newFakeAgent()
	defer ts.Close()
	cfg := twoServerConfig(t, ts,
		config.AutoAttach{Server: "pia", Device: "xbox"},
		config.AutoAttach{Server: "pib", Device: "bluetooth"},
	)
	h := newHarness(t, cfg)
	psB := h.pinByServer(t, "pib", "bluetooth")
	psB.state = StateAttached

	h.m.HandleEvent("pia", proto.Event{
		Type:   proto.EventDeviceRemoved,
		Device: proto.Device{Pin: "bluetooth", BusID: "1-1.4"},
	})

	if psB.state != StateAttached {
		t.Fatalf("pib state = %q; a pia event must not mark pib absent", psB.state)
	}
}

// M2: Pause addresses (server, device), not the bare device name.
func TestPauseIsScopedToServer(t *testing.T) {
	ts, _ := newFakeAgent()
	defer ts.Close()
	cfg := twoServerConfig(t, ts,
		config.AutoAttach{Server: "pia", Device: "bluetooth"},
		config.AutoAttach{Server: "pib", Device: "bluetooth"},
	)
	h := newHarness(t, cfg)

	if err := h.m.Pause(context.Background(), "pib/bluetooth"); err != nil {
		t.Fatalf("Pause(pib/bluetooth): %v", err)
	}
	if !h.m.IsPaused("pib/bluetooth") {
		t.Fatal("pib/bluetooth should be paused")
	}
	if h.m.IsPaused("pia/bluetooth") {
		t.Fatal("pia/bluetooth must not be paused by the pib pause")
	}
}

func TestPinsAreServerQualified(t *testing.T) {
	ts, _ := newFakeAgent()
	defer ts.Close()
	cfg := twoServerConfig(t, ts,
		config.AutoAttach{Server: "pia", Device: "xbox"},
		config.AutoAttach{Server: "pib", Device: "xbox"},
	)
	h := newHarness(t, cfg)
	got := h.m.Pins()
	want := []string{"pia/xbox", "pib/xbox"}
	if len(got) != len(want) {
		t.Fatalf("Pins() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Pins() = %v, want %v", got, want)
		}
	}
}

// Backoff must be honoured while the API is down, not re-doubled every
// ReconcileInterval by the /healthz probe.
func TestBackoffHonouredWhileAPIDown(t *testing.T) {
	ts, fa := newFakeAgent(presentDevice("xbox", "1-1.4", config.ModeAlways, proto.StateExported))
	defer ts.Close()
	h := newHarness(t, testConfig(t, ts, config.AutoAttach{Server: "pi", Device: "xbox"}))
	fa.setHealthy(false)

	h.reconcile(t)
	first := h.pinState(t, "xbox").backoff
	h.reconcile(t)
	second := h.pinState(t, "xbox").backoff

	if first != time.Second {
		t.Fatalf("first backoff = %s, want 1s", first)
	}
	if second != first {
		t.Fatalf("backoff grew while waiting: %s -> %s", first, second)
	}
}

// A Pause that arrives while Attach is in flight must win: the freshly
// attached port is detached again rather than left behind a pause.
func TestPauseDuringAttachDetaches(t *testing.T) {
	ts, _ := newFakeAgent(presentDevice("xbox", "1-1.4", config.ModeAlways, proto.StateExported))
	defer ts.Close()
	h := newHarness(t, testConfig(t, ts, config.AutoAttach{Server: "pi", Device: "xbox"}))

	started := make(chan struct{})
	release := make(chan struct{})
	h.usbip.onAttach = func() { close(started); <-release }

	done := make(chan struct{})
	go func() {
		_ = h.m.Reconcile(context.Background())
		close(done)
	}()

	<-started
	if err := h.m.Pause(context.Background(), "pi/xbox"); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	close(release)
	<-done

	if ps := h.pinState(t, "xbox"); ps.state != StatePaused || !ps.paused {
		t.Fatalf("state = %+v, want paused", ps)
	}
	if h.usbip.detachCount() == 0 {
		t.Fatal("the port was left attached behind a pause")
	}
}

// M6: usbip-win2 may print the resolved IP, not the configured name, so a
// successful attach must be confirmed against the resolved address.
func TestAttachConfirmMatchesResolvedIP(t *testing.T) {
	ts, _ := newFakeAgent(presentDevice("xbox", "1-1.4", config.ModeAlways, proto.StateExported))
	defer ts.Close()
	h := newHarness(t, testConfig(t, ts, config.AutoAttach{Server: "pi", Device: "xbox"}))
	h.m.opt.ResolveHost = func(context.Context, string) []string { return []string{"10.0.0.5"} }
	h.usbip.reportHost = "10.0.0.5"

	h.reconcile(t)

	if ps := h.pinState(t, "xbox"); ps.state != StateAttached {
		t.Fatalf("state = %q, want attached: port printed the resolved IP", ps.state)
	}
}

// M4: the last attach error is surfaced in the status the tray renders.
func TestStatusSurfacesAttachError(t *testing.T) {
	ts, _ := newFakeAgent(presentDevice("xbox", "1-1.4", config.ModeAlways, proto.StateExported))
	defer ts.Close()
	h := newHarness(t, testConfig(t, ts, config.AutoAttach{Server: "pi", Device: "xbox"}))
	h.usbip.attachErr = usbipwin.ErrDriverBlocked

	h.reconcile(t)

	st := h.m.Status()
	if len(st) != 1 || st[0].LastError == "" {
		t.Fatalf("Status() = %+v, want a surfaced attach error", st)
	}
}

// M7: a successful attach must clear an outstanding backoff, so the pin does
// not keep reporting "backoff" until a stale wait expires.
func TestMarkAttachedClearsNextAttempt(t *testing.T) {
	ts, _ := newFakeAgent(presentDevice("xbox", "1-1.4", config.ModeAlways, proto.StateExported))
	defer ts.Close()
	h := newHarness(t, testConfig(t, ts, config.AutoAttach{Server: "pi", Device: "xbox"}))

	ps := h.pinState(t, "xbox")
	h.m.mu.Lock()
	ps.nextAttempt = h.now.Add(time.Minute)
	h.m.markAttachedLocked(ps, "1-1.4", 0, h.now)
	cleared := ps.nextAttempt.IsZero()
	h.m.mu.Unlock()

	if !cleared {
		t.Fatalf("nextAttempt = %s after a successful attach, want it cleared", ps.nextAttempt)
	}
}

// M8: a failed host lookup must not be cached as an empty result for the whole
// TTL, or a port printing the resolved IP would not be matched for a minute.
func TestResolveHostDoesNotCacheFailure(t *testing.T) {
	ts, _ := newFakeAgent()
	defer ts.Close()
	h := newHarness(t, testConfig(t, ts, config.AutoAttach{Server: "pi", Device: "xbox"}))

	calls := 0
	h.m.opt.ResolveHost = func(context.Context, string) []string {
		calls++
		if calls == 1 {
			return nil
		}
		return []string{"10.0.0.5"}
	}

	ctx := context.Background()
	if got := h.m.resolveHost(ctx, "pi.local"); len(got) != 0 {
		t.Fatalf("first resolve = %v, want empty (the lookup failed)", got)
	}
	got := h.m.resolveHost(ctx, "pi.local")
	if len(got) == 0 {
		t.Fatalf("second resolve = %v, want the address: a failed lookup must not be cached", got)
	}
	if calls != 2 {
		t.Fatalf("resolver calls = %d, want 2 (the failure must not be cached)", calls)
	}
}
