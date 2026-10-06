//go:build !windows

package execx

import "syscall"

// childSysProcAttr is nil off Windows: there is no console window to suppress
// and SysProcAttr carries no relevant field, so the runner behaves exactly as
// before on Linux.
func childSysProcAttr() *syscall.SysProcAttr {
	return nil
}
