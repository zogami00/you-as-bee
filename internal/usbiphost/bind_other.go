//go:build !linux

package usbiphost

// New returns a Binder that reports ErrUnsupported for every operation. The
// usbip-host driver only exists on Linux; the agent builds and its logic is
// tested on other platforms through fakes.
func New() *Binder { return &Binder{Unsupported: true} }
