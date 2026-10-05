package client

import (
	"context"
	"time"

	"github.com/zogami00/you-as-bee/internal/config"
)

// watchEvents consumes GET /v1/events for one server until ctx is cancelled,
// reconnecting with the same exponential backoff policy the supervisor uses.
// A dropped stream is recorded as an outage so a vanished port immediately
// afterwards is classified as a transient drop rather than an external detach.
func (m *Manager) watchEvents(ctx context.Context, s config.ServerConfig) {
	initial := m.opt.Config.Reconnect.Initial.Duration()
	maxBackoff := m.opt.Config.Reconnect.Max.Duration()
	if initial <= 0 {
		initial = time.Second
	}
	if maxBackoff < initial {
		maxBackoff = initial
	}

	backoff := initial
	for {
		if ctx.Err() != nil {
			return
		}
		cli := m.streams[s.Name]
		if cli == nil {
			return
		}

		events, err := cli.Events(ctx)
		if err != nil {
			m.markSSEDown(s.Name)
			if !m.wait(ctx, backoff) {
				return
			}
			backoff = growBackoff(backoff, maxBackoff)
			continue
		}

		m.markSSEUp(s.Name)
		backoff = initial

		for ev := range events {
			m.HandleEvent(ev)
		}

		m.markSSEDown(s.Name)
		if ctx.Err() != nil {
			return
		}
		if !m.wait(ctx, backoff) {
			return
		}
		backoff = growBackoff(backoff, maxBackoff)
	}
}

// wait blocks for d or until ctx is cancelled. It reports false when ctx ended.
func (m *Manager) wait(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-m.opt.After(d):
		return true
	}
}

func growBackoff(d, maxBackoff time.Duration) time.Duration {
	d *= 2
	if d > maxBackoff {
		d = maxBackoff
	}
	return d
}

func (m *Manager) markSSEUp(name string) {
	m.mu.Lock()
	m.sseDown[name] = false
	m.mu.Unlock()
}

func (m *Manager) markSSEDown(name string) {
	m.mu.Lock()
	m.sseDown[name] = true
	m.lastOutage = m.opt.Now()
	m.mu.Unlock()
}

// SSEDown reports whether the event stream for a server is currently down.
func (m *Manager) SSEDown(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sseDown[name]
}
