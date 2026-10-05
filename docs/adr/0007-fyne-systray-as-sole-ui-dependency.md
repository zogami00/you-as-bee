# 7. fyne.io/systray for the Windows tray, Windows-only

- Status: Accepted
- Date: 2026-10-05

## Context

The Windows client needs a notification-area UI to show device status and to
attach/detach without a console. Writing Win32 shell_NotifyIcon plumbing by hand
is possible but wasteful for a small tray menu. The tray is a Windows-only
feature; the Linux agent is headless.

## Decision

Use **`fyne.io/systray`** as the tray implementation, the only third-party UI
dependency, confined to the Windows `yab` build (`internal/tray/tray_windows.go`
with a no-op `tray_other.go`). `golang.org/x/sys` is used for direct Windows
syscalls (token elevation, the Secure Boot registry read) and `godbus/dbus` is
an indirect, Linux-only requirement of systray; neither is a UI framework.

## Consequences

- The tray is a small, replaceable layer behind the `tray.Controller`
  interface; the supervisor does not depend on it.
- `CGO_ENABLED=0` builds still work: systray is pure Go on Windows.
- The dependency is isolated to the Windows binary, so the Linux agent stays
  stdlib-only.
- If systray becomes unmaintained, only `internal/tray/tray_windows.go` needs
  replacing.
