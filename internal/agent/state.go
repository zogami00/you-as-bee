package agent

// State is the lifecycle state of a pinned device as tracked by the reconciler.
type State int

// Device lifecycle states.
const (
	// Absent means the pinned device is not attached.
	Absent State = iota
	// Present means the device is attached but not exported.
	Present
	// Binding means the device is being moved to usbip-host.
	Binding
	// Exported means the device is exported and available to clients.
	Exported
	// Attached means a client has attached to the exported device.
	Attached
	// Backoff means a bind attempt failed and the agent is waiting to retry.
	Backoff
	// Quarantined means repeated failures paused all work on the device.
	Quarantined
	// Disabled means the device was administratively disabled.
	Disabled
)

// String returns the lower-case name of the state.
func (s State) String() string {
	switch s {
	case Absent:
		return "absent"
	case Present:
		return "present"
	case Binding:
		return "binding"
	case Exported:
		return "exported"
	case Attached:
		return "attached"
	case Backoff:
		return "backoff"
	case Quarantined:
		return "quarantined"
	case Disabled:
		return "disabled"
	default:
		return "unknown"
	}
}
