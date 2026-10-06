// Package tray renders the Windows notification-area icon and menu for the
// you-as-bee client. The portable half of the package (this file) defines the
// controller surface; the platform files provide Run.
package tray

import "context"

// Device is one menu entry the tray renders.
type Device struct {
	// Pin is the stable device id used by attach/detach.
	Pin string
	// Name is the human-facing label.
	Name string
	// Status is the current lifecycle state (from client.PinStatus).
	Status string
	// Attached reports whether the device is currently attached locally.
	Attached bool
	// Paused reports whether auto-attach is user-paused.
	Paused bool
	// LastError is the most recent attach/confirm failure, if any.
	LastError string
	// PauseReason explains why auto-attach is paused.
	PauseReason string
}

// Controller is the supervisor surface the tray drives. It is implemented by
// an adapter over *client.Manager, so the tray package does not depend on the
// supervisor directly.
type Controller interface {
	// Devices returns the current device list in config order.
	Devices() []Device
	// Attach attaches a device by pin.
	Attach(pin string) error
	// Detach detaches a device by pin and marks it user-paused.
	Detach(pin string) error
	// Elevated reports whether the process is running elevated.
	Elevated() bool
	// RestartElevated relaunches the process through UAC.
	RestartElevated() error
	// OpenUI opens the local web UI in the user's default browser, de-elevated.
	OpenUI() error
}

// Run renders the tray until ctx is cancelled. On platforms without a tray
// implementation it blocks until ctx ends.
func Run(ctx context.Context, c Controller) { run(ctx, c) }
