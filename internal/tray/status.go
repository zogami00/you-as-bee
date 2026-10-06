package tray

import (
	"fmt"
	"strings"
	"sync"
)

// Device status strings mirrored from client.PinStatus.State. The tray package
// deliberately does not import internal/client (see tray.go), so the values are
// repeated here. They are part of the adapter contract: cmd/yab copies
// ps.State into Device.Status unchanged.
const (
	statusIdle        = "idle"
	statusAbsent      = "absent"
	statusAttached    = "attached"
	statusBackoff     = "backoff"
	statusPaused      = "paused"
	statusNetworkDown = "network_down"
)

// summaryText renders the tray status line from the live device states: a short
// count of what is attached, absent, paused, backing off or in error, plus a
// reachability hint when a server is down.
//
// It is a pure function in a platform-neutral file so it can be unit-tested
// without a display; the systray wiring around it is only compile-checked. It
// always returns a summary (even for an empty device list), so a healthy run
// never leaves the line stuck on its startup value.
func summaryText(devices []Device) string {
	if len(devices) == 0 {
		return "no devices"
	}

	var attached, absent, paused, backoff, errored, network, idle int
	for _, d := range devices {
		switch d.Status {
		case statusNetworkDown:
			network++
		case statusBackoff:
			backoff++
		case statusAttached:
			attached++
		case statusPaused:
			paused++
		case statusAbsent:
			absent++
		default:
			idle++
		}
		// LastError is counted separately only when the state does not already
		// surface the failure: backoff and network_down imply it.
		if d.LastError != "" && d.Status != statusBackoff && d.Status != statusNetworkDown {
			errored++
		}
	}

	var parts []string
	if network > 0 {
		parts = append(parts, "server unreachable")
	}
	if attached > 0 {
		parts = append(parts, fmt.Sprintf("%d attached", attached))
	}
	if backoff > 0 {
		parts = append(parts, fmt.Sprintf("%d backoff", backoff))
	}
	if errored > 0 {
		parts = append(parts, fmt.Sprintf("%d error", errored))
	}
	if paused > 0 {
		parts = append(parts, fmt.Sprintf("%d paused", paused))
	}
	if absent > 0 {
		parts = append(parts, fmt.Sprintf("%d absent", absent))
	}
	if len(parts) == 0 {
		// Every device is in a nominal (idle/unknown) state.
		return "all idle"
	}
	return strings.Join(parts, ", ")
}

// statusState keeps a one-off menu-action error so the periodic refresh cannot
// silently overwrite it with a healthy summary a tick later.
//
// Reconciliation choice: the status line is always the live device summary, so
// state-derived failures (backoff/error/absent/server-down) are visible for as
// long as the state lasts. A failure from a menu action (Open web UI, restart,
// attach/detach) has no persistent device state, so it is shown beside the
// summary and kept until a later successful action clears it. It is never
// dropped merely because a refresh tick arrived.
type statusState struct {
	mu        sync.Mutex
	actionErr string
}

// fail records a menu-action error. A nil error is ignored.
func (s *statusState) fail(err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	s.actionErr = err.Error()
	s.mu.Unlock()
}

// clear forgets a previously recorded menu-action error after a successful
// action.
func (s *statusState) clear() {
	s.mu.Lock()
	s.actionErr = ""
	s.mu.Unlock()
}

// line renders the status line from the live device summary and any pending
// menu-action error.
func (s *statusState) line(summary string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.actionErr == "" {
		return summary
	}
	return "error: " + s.actionErr + " - " + summary
}
