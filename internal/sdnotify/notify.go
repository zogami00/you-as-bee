// Package sdnotify implements the systemd sd_notify protocol with the standard
// library only.
package sdnotify

// Ready tells systemd the service finished starting.
func Ready() error { return Notify("READY=1") }

// Watchdog resets the systemd watchdog timer.
func Watchdog() error { return Notify("WATCHDOG=1") }

// Status sets the human-readable service status line.
func Status(text string) error { return Notify("STATUS=" + text) }
