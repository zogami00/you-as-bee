// Package client is the Windows-side supervisor: it keeps the configured
// devices attached over USB/IP, consuming the agent's SSE stream and backing
// off against the network and against usbip failures.
package client

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"math/rand"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zogami00/you-as-bee/internal/api"
	"github.com/zogami00/you-as-bee/internal/config"
	"github.com/zogami00/you-as-bee/internal/proto"
	"github.com/zogami00/you-as-bee/internal/usbipwin"
)

// Device states as reported by Manager.Status. They describe the client's view
// of one auto_attach entry.
const (
	StateIdle        = "idle"
	StateAbsent      = "absent"
	StateAttached    = "attached"
	StateBackoff     = "backoff"
	StatePaused      = "paused"
	StateNetworkDown = "network_down"
)

// jitterFraction is the +/- fraction applied to every backoff delay.
const jitterFraction = 0.2

// ErrUnknownPin is returned when a pin id selects no configured auto_attach
// entry.
var ErrUnknownPin = errors.New("client: unknown device pin")

// USBIP is the usbip-win2 surface the supervisor needs. It is satisfied by
// *usbipwin.Tool.
type USBIP interface {
	Attach(ctx context.Context, host, busid string) error
	Detach(ctx context.Context, port int) error
	Port(ctx context.Context) ([]usbipwin.PortEntry, error)
	ListRemote(ctx context.Context, host string) ([]usbipwin.RemoteDevice, error)
}

// Options configures a Manager.
type Options struct {
	Config config.ClientConfig
	USBIP  USBIP
	Log    *slog.Logger

	// Now returns the current time. Defaults to time.Now.
	Now func() time.Time
	// After returns a channel that fires after d. Defaults to time.After.
	After func(time.Duration) <-chan time.Time
	// Rand returns a value in [0,1) used for backoff jitter. Defaults to
	// math/rand.Float64.
	Rand func() float64

	// StableAfter is how long an attachment must last before the backoff
	// resets. Defaults to 60s.
	StableAfter time.Duration
	// OutageWindow is how recent an API/SSE outage must be for a vanished port
	// to count as a transient drop. Defaults to twice reconnect.max.
	OutageWindow time.Duration
	// RespectExternalDetach pauses auto-attach when a port disappears without a
	// recent outage (i.e. it was detached outside usbip). Defaults to true.
	RespectExternalDetach *bool

	// Notify receives user-facing notifications. May be nil.
	Notify func(pin, message string)
	// Health overrides the HTTP client used for /healthz probes.
	Health *http.Client
	// ReconcileInterval is the Run loop period. Defaults to 5s.
	ReconcileInterval time.Duration
}

// PinStatus is a snapshot of one auto_attach entry for the tray and CLI.
type PinStatus struct {
	Pin    string
	Server string
	State  string
	BusID  string
	Port   int
	Paused bool
}

type pinState struct {
	attach config.AutoAttach
	server config.ServerConfig

	paused        bool
	backoff       time.Duration
	nextAttempt   time.Time
	attachedSince time.Time
	everAttached  bool
	port          int
	busid         string
	state         string
}

// Manager keeps every configured auto_attach entry attached.
type Manager struct {
	opt     Options
	respect bool

	mu         sync.Mutex
	pins       map[string]*pinState
	clients    map[string]*api.Client
	streams    map[string]*api.Client
	sseDown    map[string]bool
	lastOutage time.Time
	wake       chan struct{}
}

// New builds a Manager. The config is expected to have been validated already
// (config.Load does this), so servers carry resolved ports and tokens.
func New(opt Options) (*Manager, error) {
	if opt.USBIP == nil {
		return nil, errors.New("client: USBIP is required")
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.After == nil {
		opt.After = time.After
	}
	if opt.Rand == nil {
		opt.Rand = rand.Float64
	}
	if opt.StableAfter <= 0 {
		opt.StableAfter = 60 * time.Second
	}
	if opt.OutageWindow <= 0 {
		opt.OutageWindow = 2 * opt.Config.Reconnect.Max.Duration()
		if opt.OutageWindow <= 0 {
			opt.OutageWindow = 2 * 30 * time.Second
		}
	}
	if opt.ReconcileInterval <= 0 {
		opt.ReconcileInterval = 5 * time.Second
	}
	respect := true
	if opt.RespectExternalDetach != nil {
		respect = *opt.RespectExternalDetach
	}

	m := &Manager{
		opt:     opt,
		respect: respect,
		pins:    make(map[string]*pinState),
		clients: make(map[string]*api.Client),
		streams: make(map[string]*api.Client),
		sseDown: make(map[string]bool),
		wake:    make(chan struct{}, 1),
	}

	for _, s := range opt.Config.Servers {
		base := baseURL(s)
		m.clients[s.Name] = api.NewClient(base, s.Token, opt.Config.CommandTimeout.Duration())
		// A streaming client must not carry an overall request timeout, which
		// would kill the long-lived SSE response.
		stream := api.NewClient(base, s.Token, 0)
		stream.HTTP = &http.Client{}
		m.streams[s.Name] = stream
	}
	for _, a := range opt.Config.AutoAttach {
		srv, ok := m.serverByNameLocked(a.Server)
		if !ok {
			continue
		}
		m.pins[pinKey(a)] = &pinState{attach: a, server: srv, state: StateIdle, port: -1}
	}
	return m, nil
}

func baseURL(s config.ServerConfig) string {
	return "http://" + net.JoinHostPort(s.Host, strconv.Itoa(s.APIPort))
}

func pinKey(a config.AutoAttach) string { return a.Server + "\x00" + a.Device }

func (m *Manager) serverByNameLocked(name string) (config.ServerConfig, bool) {
	for _, s := range m.opt.Config.Servers {
		if s.Name == name {
			return s, true
		}
	}
	return config.ServerConfig{}, false
}

func (m *Manager) now() time.Time { return m.opt.Now() }

// Pins returns the configured pin ids in config order.
func (m *Manager) Pins() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.opt.Config.AutoAttach))
	for _, a := range m.opt.Config.AutoAttach {
		out = append(out, a.Device)
	}
	return out
}

// Status returns a snapshot of every auto_attach entry.
func (m *Manager) Status() []PinStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]PinStatus, 0, len(m.opt.Config.AutoAttach))
	for _, a := range m.opt.Config.AutoAttach {
		ps := m.pins[pinKey(a)]
		if ps == nil {
			continue
		}
		out = append(out, PinStatus{
			Pin:    ps.attach.Device,
			Server: ps.attach.Server,
			State:  ps.state,
			BusID:  ps.busid,
			Port:   ps.port,
			Paused: ps.paused,
		})
	}
	return out
}

func (m *Manager) lookupLocked(id string) (*pinState, bool) {
	for _, ps := range m.pins {
		if ps.attach.Device == id || (ps.busid != "" && ps.busid == id) {
			return ps, true
		}
	}
	return nil, false
}

// Pause marks a pin user-paused and detaches its port if one is known. The
// pause lives only in memory, so a restart restores auto-attach.
func (m *Manager) Pause(ctx context.Context, pin string) error {
	m.mu.Lock()
	ps, ok := m.lookupLocked(pin)
	if !ok {
		m.mu.Unlock()
		return ErrUnknownPin
	}
	ps.paused = true
	port := ps.port
	ps.port = -1
	ps.attachedSince = time.Time{}
	ps.state = StatePaused
	m.mu.Unlock()

	if port >= 0 {
		_ = m.opt.USBIP.Detach(ctx, port)
	}
	return nil
}

// Detach is an alias for Pause, matching the CLI verb.
func (m *Manager) Detach(ctx context.Context, pin string) error { return m.Pause(ctx, pin) }

// Resume clears the user pause and resets the backoff.
func (m *Manager) Resume(pin string) error {
	m.mu.Lock()
	ps, ok := m.lookupLocked(pin)
	if !ok {
		m.mu.Unlock()
		return ErrUnknownPin
	}
	ps.paused = false
	ps.backoff = 0
	ps.nextAttempt = time.Time{}
	ps.everAttached = false
	m.mu.Unlock()
	m.signal()
	return nil
}

// IsPaused reports whether a pin is user-paused.
func (m *Manager) IsPaused(pin string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	ps, ok := m.lookupLocked(pin)
	return ok && ps.paused
}

// HandleEvent applies an agent event to the supervisor state. A device_added
// event resumes a paused pin (a re-plug overrides an external-detach pause) and
// makes the next attach immediate.
func (m *Manager) HandleEvent(ev proto.Event) {
	id := ev.Device.Pin
	if id == "" {
		id = ev.Device.BusID
	}
	m.mu.Lock()
	ps, ok := m.lookupLocked(id)
	if !ok {
		m.mu.Unlock()
		return
	}
	switch ev.Type {
	case proto.EventDeviceAdded:
		ps.paused = false
		ps.backoff = 0
		ps.nextAttempt = time.Time{}
		ps.everAttached = false
	case proto.EventDeviceRemoved:
		ps.port = -1
		ps.busid = ""
		ps.attachedSince = time.Time{}
		ps.everAttached = false
		ps.state = StateAbsent
	}
	m.mu.Unlock()
	m.signal()
}

func (m *Manager) signal() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

// Reconcile runs one pass over every configured pin.
func (m *Manager) Reconcile(ctx context.Context) error {
	m.mu.Lock()
	pins := make([]*pinState, 0, len(m.pins))
	for _, ps := range m.pins {
		pins = append(pins, ps)
	}
	m.mu.Unlock()

	for _, ps := range pins {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		m.reconcilePin(ctx, ps)
	}
	return nil
}

// Run reconciles until ctx is cancelled, waking on a timer or an SSE event.
func (m *Manager) Run(ctx context.Context) error {
	for _, s := range m.opt.Config.Servers {
		go m.watchEvents(ctx, s)
	}
	ticker := time.NewTicker(m.opt.ReconcileInterval)
	defer ticker.Stop()

	_ = m.Reconcile(ctx)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		case <-m.wake:
		}
		_ = m.Reconcile(ctx)
	}
}

func (m *Manager) reconcilePin(ctx context.Context, ps *pinState) {
	now := m.now()

	m.mu.Lock()
	if ps.paused {
		ps.state = StatePaused
		m.mu.Unlock()
		return
	}
	server := ps.server
	attach := ps.attach
	m.mu.Unlock()

	// Row 1: API unreachable (SSE dropped and /healthz fails). Back off
	// against the API only and make no attach calls.
	if !m.apiUp(ctx, server) {
		m.noteOutage(now)
		m.mu.Lock()
		m.applyBackoffLocked(ps, now)
		ps.state = StateNetworkDown
		m.mu.Unlock()
		return
	}

	cli := m.clients[server.Name]
	if cli == nil {
		return
	}
	devs, err := cli.Devices(ctx)
	if err != nil {
		m.noteOutage(now)
		m.mu.Lock()
		m.applyBackoffLocked(ps, now)
		ps.state = StateNetworkDown
		m.mu.Unlock()
		return
	}

	// Row 2: device Present:false or absent from the list -> a real unplug on
	// the Pi. Stay idle until a device_added event.
	dev, ok := findDevice(devs, attach.Device)
	if !ok || !dev.Present {
		m.mu.Lock()
		ps.state = StateAbsent
		ps.port = -1
		ps.busid = ""
		ps.attachedSince = time.Time{}
		ps.everAttached = false
		ps.backoff = 0
		m.mu.Unlock()
		return
	}

	// Ensure the device is exported on the agent before attaching.
	if dev.Mode == config.ModeOnDemand && dev.State != proto.StateExported && dev.State != proto.StateInUse {
		if err := cli.Export(ctx, dev.Pin, false); err == nil {
			if refreshed, rerr := cli.Device(ctx, dev.Pin); rerr == nil {
				dev = refreshed
			}
		}
	}

	// Always use the busid the agent reports now; never a cached busid.
	busid := dev.BusID
	if busid == "" {
		m.mu.Lock()
		ps.state = StateAbsent
		m.mu.Unlock()
		return
	}

	ports, perr := m.opt.USBIP.Port(ctx)
	if perr != nil {
		m.mu.Lock()
		m.applyBackoffLocked(ps, now)
		ps.state = StateBackoff
		m.mu.Unlock()
		return
	}
	if p, found := findPort(ports, server.Host, busid); found {
		m.mu.Lock()
		m.markAttachedLocked(ps, busid, p.Port, now)
		m.mu.Unlock()
		return
	}

	// The port vanished. Decide transient drop vs external detach.
	m.mu.Lock()
	wasAttached := ps.everAttached
	waiting := now.Before(ps.nextAttempt)
	m.mu.Unlock()

	if wasAttached && !m.recentOutage(now) && m.respect {
		// Row 4: attached, no outage, detached outside usbip -> pause + notify.
		_ = m.Pause(ctx, attach.Device)
		if m.opt.Notify != nil {
			m.opt.Notify(attach.Device, "device was detached outside usbip; auto-attach paused")
		}
		return
	}

	if waiting {
		// Row 3 (or a failing attach): back off before retrying.
		m.mu.Lock()
		ps.state = StateBackoff
		m.mu.Unlock()
		return
	}

	// Row 3: transient drop, or a first attach. Detach a stale vhci port for
	// this host+busid before re-attaching, then attach and confirm.
	m.detachStale(ctx, server.Host, busid)
	if err := m.opt.USBIP.Attach(ctx, server.Host, busid); err != nil {
		m.mu.Lock()
		m.applyBackoffLocked(ps, now)
		ps.state = StateBackoff
		m.mu.Unlock()
		return
	}

	confirmed, cerr := m.opt.USBIP.Port(ctx)
	if cerr == nil {
		if p, found := findPort(confirmed, server.Host, busid); found {
			m.mu.Lock()
			m.markAttachedLocked(ps, busid, p.Port, now)
			m.mu.Unlock()
			return
		}
	}
	// The command exited zero but the port is not there: treat as a failure.
	m.mu.Lock()
	m.applyBackoffLocked(ps, now)
	ps.state = StateBackoff
	m.mu.Unlock()
}

func (m *Manager) markAttachedLocked(ps *pinState, busid string, port int, now time.Time) {
	ps.busid = busid
	ps.port = port
	if ps.attachedSince.IsZero() {
		ps.attachedSince = now
	}
	if now.Sub(ps.attachedSince) >= m.opt.StableAfter {
		ps.backoff = 0
	}
	ps.everAttached = true
	ps.state = StateAttached
}

func (m *Manager) applyBackoffLocked(ps *pinState, now time.Time) {
	ps.attachedSince = time.Time{}
	ps.port = -1

	initial := m.opt.Config.Reconnect.Initial.Duration()
	maxBackoff := m.opt.Config.Reconnect.Max.Duration()
	if initial <= 0 {
		initial = time.Second
	}
	if maxBackoff < initial {
		maxBackoff = initial
	}

	if ps.backoff <= 0 {
		ps.backoff = initial
	} else {
		ps.backoff *= 2
		if ps.backoff > maxBackoff {
			ps.backoff = maxBackoff
		}
	}
	jitter := 1 + (m.opt.Rand()-0.5)*2*jitterFraction
	ps.nextAttempt = now.Add(time.Duration(float64(ps.backoff) * jitter))
}

func (m *Manager) noteOutage(now time.Time) {
	m.mu.Lock()
	m.lastOutage = now
	m.mu.Unlock()
}

func (m *Manager) recentOutage(now time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return !m.lastOutage.IsZero() && now.Sub(m.lastOutage) < m.opt.OutageWindow
}

func (m *Manager) apiUp(ctx context.Context, s config.ServerConfig) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL(s)+"/healthz", nil)
	if err != nil {
		return false
	}
	resp, err := m.healthClient().Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode == http.StatusOK
}

func (m *Manager) healthClient() *http.Client {
	if m.opt.Health != nil {
		return m.opt.Health
	}
	return &http.Client{Timeout: 3 * time.Second}
}

// detachStale detaches any local vhci port already holding busid, whatever
// host it points at. A port for the same host+busid would have matched
// findPort above; the remaining case is a stale port left by a previous
// session or a renamed/old server address.
func (m *Manager) detachStale(ctx context.Context, host, busid string) {
	ports, err := m.opt.USBIP.Port(ctx)
	if err != nil {
		return
	}
	for _, p := range ports {
		if p.BusID == busid {
			_ = m.opt.USBIP.Detach(ctx, p.Port)
		}
	}
}

func findDevice(devs []proto.Device, id string) (proto.Device, bool) {
	for _, d := range devs {
		if d.Pin == id {
			return d, true
		}
	}
	return proto.Device{}, false
}

func findPort(ports []usbipwin.PortEntry, host, busid string) (usbipwin.PortEntry, bool) {
	for _, p := range ports {
		if p.BusID == busid && hostMatches(p.Host, host) {
			return p, true
		}
	}
	return usbipwin.PortEntry{}, false
}

func hostMatches(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

// ServerStatus describes one configured server's reachability for `yab status`.
type ServerStatus struct {
	Name        string `json:"name"`
	Host        string `json:"host"`
	APIPort     int    `json:"api_port"`
	Reachable   bool   `json:"reachable"`
	TokenValid  bool   `json:"token_valid"`
	DeviceCount int    `json:"device_count"`
	Err         string `json:"error,omitempty"`
}

// CheckServers probes every configured server with a bounded request.
func (m *Manager) CheckServers(ctx context.Context) []ServerStatus {
	out := make([]ServerStatus, 0, len(m.opt.Config.Servers))
	for _, s := range m.opt.Config.Servers {
		st := ServerStatus{Name: s.Name, Host: s.Host, APIPort: s.APIPort}
		cli := m.clients[s.Name]
		if cli == nil {
			out = append(out, st)
			continue
		}
		devs, err := cli.Devices(ctx)
		if err == nil {
			st.Reachable = true
			st.TokenValid = true
			st.DeviceCount = len(devs)
			out = append(out, st)
			continue
		}
		var ae *api.APIError
		if errors.As(err, &ae) {
			st.Reachable = true
			st.TokenValid = ae.Status != http.StatusUnauthorized
			st.Err = ae.Error()
		} else {
			st.Err = err.Error()
		}
		out = append(out, st)
	}
	return out
}

// Client returns the typed API client for a named server (for doctor and the
// CLI). It is nil when the server is unknown.
func (m *Manager) Client(name string) *api.Client { return m.clients[name] }

// FindServer returns the configured server with the given name.
func (m *Manager) FindServer(name string) (config.ServerConfig, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.serverByNameLocked(name)
}

// ServerForPin resolves the server that owns a pin id.
func (m *Manager) ServerForPin(pin string) (config.ServerConfig, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ps, ok := m.lookupLocked(pin)
	if !ok {
		return config.ServerConfig{}, ErrUnknownPin
	}
	return ps.server, nil
}

// Config returns the manager's config.
func (m *Manager) Config() config.ClientConfig { return m.opt.Config }
