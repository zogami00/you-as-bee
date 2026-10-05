//go:build linux

package usbiphost

import (
	"os"

	"github.com/zogami00/you-as-bee/internal/execx"
)

// New returns the production Binder for Linux, backed by the real sysfs tree
// and the os/exec runner.
func New() *Binder {
	return &Binder{
		FS:       osFS{},
		Runner:   execx.New(),
		Root:     "/sys",
		Modprobe: "/sbin/modprobe",
		IsRoot:   func() bool { return os.Geteuid() == 0 },
	}
}

// osFS is the SysFS implementation reading and writing the real filesystem.
type osFS struct{}

func (osFS) ReadFile(name string) ([]byte, error) { return os.ReadFile(name) }

func (osFS) WriteFile(name string, data []byte) error {
	return os.WriteFile(name, data, 0o200)
}

func (osFS) ReadLink(name string) (string, error) { return os.Readlink(name) }

func (osFS) Exists(name string) bool {
	_, err := os.Stat(name)
	return err == nil
}
