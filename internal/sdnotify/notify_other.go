//go:build !linux

package sdnotify

// Notify is a no-op off Linux: systemd only exists there.
func Notify(string) error { return nil }
