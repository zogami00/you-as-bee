// Package proto defines the JSON API contract shared by the Pi agent (yabd)
// and the Windows client (yab). It is deliberately self-contained and depends
// only on the Go standard library.
package proto

import "time"

// Device lifecycle states as reported by the agent.
const (
	StateUnexported = "unexported"
	StateExported   = "exported"
	StateInUse      = "in_use"
	StateAbsent     = "absent"
	StateError      = "error"
)

// Server-sent event names.
const (
	EventDeviceAdded   = "device_added"
	EventDeviceRemoved = "device_removed"
	EventStateChanged  = "state_changed"
)

// Device is a single USB device known to the agent.
type Device struct {
	// Pin is the stable device identity the agent pins exports to.
	Pin string `json:"pin"`
	// BusID is the USB/IP bus id, e.g. "1-1.4".
	BusID string `json:"busid"`
	// VID and PID are 4-hex-digit identifiers.
	VID string `json:"vid"`
	PID string `json:"pid"`
	// Serial, Product and Driver are informational; Driver is the kernel driver
	// currently bound to the device (empty when unbound).
	Serial  string `json:"serial,omitempty"`
	Product string `json:"product,omitempty"`
	Driver  string `json:"driver,omitempty"`
	// Present reports whether the device is physically attached right now.
	Present bool `json:"present"`
	// State is one of the State* constants.
	State string `json:"state"`
	// Mode is the configured export mode ("always" or "on_demand").
	Mode string `json:"mode"`
	// LastError carries the most recent bind/unbind failure for the pin, and is
	// cleared by a successful bind or an explicit reset. Omitted when empty.
	LastError string `json:"last_error,omitempty"`
}

// Info describes the agent itself (GET /v1/info).
type Info struct {
	Version   string `json:"version"`
	Hostname  string `json:"hostname"`
	UptimeSec int64  `json:"uptime_sec"`
	UsbipdUp  bool   `json:"usbipd_up"`
}

// Error is the standard error body returned by the management API.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ListDevicesResponse is the body of GET /v1/devices.
type ListDevicesResponse struct {
	Devices []Device `json:"devices"`
}

// Event is one server-sent event (GET /v1/events).
type Event struct {
	// Type is one of the Event* constants.
	Type   string    `json:"type"`
	Device Device    `json:"device"`
	At     time.Time `json:"at"`
}

// LogEntry is one structured log record served by GET /v1/logs.
type LogEntry struct {
	// Seq is a monotonically increasing sequence number, starting at 1.
	Seq uint64 `json:"seq"`
	// Time is when the record was emitted.
	Time time.Time `json:"time"`
	// Level is the lower-case slog level name ("info", "warn", "error").
	Level string `json:"level"`
	// Msg is the formatted log message.
	Msg string `json:"msg"`
	// Attrs are the structured attributes attached to the record.
	Attrs map[string]string `json:"attrs,omitempty"`
}

// LogsResponse is the body of GET /v1/logs.
type LogsResponse struct {
	// Entries are the log records after the requested sequence number, oldest
	// first.
	Entries []LogEntry `json:"entries"`
	// Next is the sequence number to pass as the next `after` query value.
	Next uint64 `json:"next"`
}
