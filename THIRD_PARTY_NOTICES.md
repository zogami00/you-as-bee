# Third-party notices

`you-as-bee` itself is licensed under the MIT License (see `LICENSE`). It
depends on the components below.

## Compiled Go dependencies

| Module | License | Used by | Notes |
| --- | --- | --- | --- |
| `fyne.io/systray` | BSD-3-Clause | `yab` (Windows client) only | System-tray icon and menu. Pure Go on Windows; the `CGO_ENABLED=0` Windows build is verified. |
| `golang.org/x/sys` | BSD-3-Clause | `yab` (Windows client) only | Windows syscalls: process-token elevation, `ShellExecuteW` and the registry. |
| `github.com/godbus/dbus/v5` | BSD-2-Clause | indirect, via `fyne.io/systray` | Pulled in by systray's Linux backend. It is not compiled into any Windows build; `GOOS=linux yab` does not import systray either. |

`yabd` (the Raspberry Pi agent) is built with `CGO_ENABLED=0` and, like the
foundation code, links no third-party Go modules. The third-party modules above
are reachable only from `cmd/yab` on Windows.

## External programs invoked at runtime (not linked)

`you-as-bee` orchestrates the existing USB/IP toolchain. These programs are
invoked as **separate processes via their command-line interfaces**. They are
**not** linked, statically or dynamically, into any `you-as-bee` binary, and
their source code is not included or modified in this repository.

| Program | Platform | License | Role |
| --- | --- | --- | --- |
| `usbip` | Linux | GPL-2.0 | USB/IP client/server CLI shipped by the Linux kernel project; used to list, bind, unbind and export devices. |
| `usbipd` | Linux | GPL-2.0 | USB/IP server daemon shipped by the Linux kernel project. |
| `usbip-win2` (`usbip.exe`, kernel driver) | Windows | GPL-3.0 | Windows USB/IP client and driver; used to attach exported devices so the PC sees them as local USB devices. |

Because these are separate programs communicating over documented interfaces
(USB/IP protocol and process invocation) and are not combined with
`you-as-bee` into a single work, `you-as-bee`'s MIT license is not affected by
their copyleft terms. Users who redistribute or install these programs are
responsible for complying with their respective licenses.
