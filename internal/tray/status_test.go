package tray

import (
	"errors"
	"testing"
)

// These tests cover summaryText and statusState, the pure parts of the tray
// status line. The systray wiring in tray_windows.go needs a display and is
// only compile-checked; it is deliberately not exercised here.

func TestSummaryText(t *testing.T) {
	tests := []struct {
		name    string
		devices []Device
		want    string
	}{
		{"empty", nil, "no devices"},
		{"empty slice", []Device{}, "no devices"},
		{
			"all idle",
			[]Device{{Status: statusIdle}, {Status: statusIdle}},
			"all idle",
		},
		{
			"attached only",
			[]Device{{Status: statusAttached}, {Status: statusAttached}},
			"2 attached",
		},
		{
			"attached and backoff",
			[]Device{{Status: statusAttached}, {Status: statusBackoff, LastError: "no route"}},
			"1 attached, 1 backoff",
		},
		{
			"backoff does not double count as error",
			[]Device{{Status: statusBackoff, LastError: "no route"}},
			"1 backoff",
		},
		{
			"error while attached",
			[]Device{{Status: statusAttached, LastError: "confirm failed"}},
			"1 attached, 1 error",
		},
		{
			"server unreachable hint",
			[]Device{{Status: statusNetworkDown, LastError: "dial tcp: refused"}, {Status: statusNetworkDown}},
			"server unreachable",
		},
		{
			"unreachable with attached",
			[]Device{{Status: statusNetworkDown}, {Status: statusAttached}},
			"server unreachable, 1 attached",
		},
		{
			"paused",
			[]Device{{Status: statusPaused, Paused: true, PauseReason: "user"}},
			"1 paused",
		},
		{
			"absent",
			[]Device{{Status: statusAbsent}},
			"1 absent",
		},
		{
			"paused and absent and idle",
			[]Device{{Status: statusPaused}, {Status: statusAbsent}, {Status: statusIdle}},
			"1 paused, 1 absent",
		},
		{
			"full mix in stable order",
			[]Device{
				{Status: statusAttached},
				{Status: statusBackoff, LastError: "x"},
				{Status: statusPaused},
				{Status: statusAbsent},
			},
			"1 attached, 1 backoff, 1 paused, 1 absent",
		},
		{
			"unknown status is nominal",
			[]Device{{Status: ""}},
			"all idle",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := summaryText(tt.devices); got != tt.want {
				t.Errorf("summaryText(%+v) = %q, want %q", tt.devices, got, tt.want)
			}
		})
	}
}

func TestStatusStateLine(t *testing.T) {
	st := &statusState{}
	if got := st.line("all idle"); got != "all idle" {
		t.Errorf("line with no error = %q, want %q", got, "all idle")
	}

	st.fail(errors.New("could not open a browser"))
	got := st.line("1 attached")
	want := "error: could not open a browser - 1 attached"
	if got != want {
		t.Errorf("line with an error = %q, want %q", got, want)
	}

	// A later refresh must not drop the error: the summary changes but the
	// error stays until an action clears it.
	if got := st.line("2 attached"); got != "error: could not open a browser - 2 attached" {
		t.Errorf("line after a refresh = %q, want the error to persist", got)
	}

	// A nil error must not wipe a real one.
	st.fail(nil)
	if got := st.line("all idle"); got != "error: could not open a browser - all idle" {
		t.Errorf("line after fail(nil) = %q, want the earlier error to persist", got)
	}

	st.clear()
	if got := st.line("all idle"); got != "all idle" {
		t.Errorf("line after clear = %q, want %q", got, "all idle")
	}
}
