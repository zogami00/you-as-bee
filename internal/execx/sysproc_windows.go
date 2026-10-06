//go:build windows

package execx

import "syscall"

// childSysProcAttr returns the SysProcAttr used for every child process.
//
// yabw.exe is a GUI-subsystem process with no console of its own, so a console
// child such as usbip.exe would otherwise allocate and visibly flash a new
// console window - on every reconcile tick (5 s) while a device is present, and
// on each attach/detach. CREATE_NO_WINDOW (0x08000000) tells Windows not to
// create one; HideWindow is belt-and-braces for the same result and covers the
// STARTF_USESHOWWINDOW path.
func childSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}
