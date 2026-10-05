# you-as-bee

USB-over-LAN device sharing — a VirtualHere clone.

A Raspberry Pi exports USB devices (a Bluetooth dongle and an Xbox wireless
dongle attached to a powered hub) over the LAN, so a Windows PC sees them as
real local USB devices and can use controllers connected through the Pi.

- Pi side: `yabd` — a systemd service that pins, exports and recovers devices.
- Windows side: `yab` — a tray app and CLI that auto-attaches them.
- Built on the existing Linux `usbip`/`usbipd` server and `usbip-win2`.

See `docs/` for architecture, setup and the hardware validation checklist.

## Layout

```
cmd/yabd            Raspberry Pi agent (Linux) — management API on :3241
cmd/yab             Windows client — tray + CLI
internal/config     strict, stdlib-only config loader shared by both binaries
internal/proto      JSON API contract shared by both binaries
internal/execx      shell-free external command runner (+ test fake)
internal/sysfs      USB enumeration from sysfs (injectable FS, in-memory fake)
internal/identity   stable device keys and pin matching / ambiguity detection
internal/usbiphost  usbip-host bind/unbind sequence (build-tagged, injectable)
internal/agent      reconcile loop, backoff and quarantine circuit breaker
internal/api        management HTTP API (:3241), SSE events and typed client
internal/client     Windows supervisor: auto-attach, backoff, SSE consumer
internal/usbipwin   usbip-win2 discovery, invocation and output parsing
internal/elevate    UAC elevation check and runas relaunch
internal/tray       Windows notification-area UI (no-op off Windows)
internal/sdnotify   systemd sd_notify (READY/WATCHDOG/STATUS), stdlib only
internal/version    build-time version metadata (set via -ldflags -X)
deploy/pi           systemd units, modprobe blacklist and udev rule
scripts/build.ps1   cross-build dist/ artefacts for all three targets
scripts/check.ps1   local validation gate (gofmt, vet, test, build, cross-build)
```

Ports: the management API listens on **3241**. USB/IP uses **3240** and is
deliberately not configurable.

## Windows client (yab)

`yab` reads `%ProgramData%\you-as-bee\client.json`, locates `usbip.exe` (config
`usbip_path`, then the registry, then `%ProgramFiles%\USBip`, then `PATH`) and
supervises the configured `auto_attach` devices, re-attaching them over USB/IP
and reacting to the agent's SSE event stream.

```
yab tray                       default when run with no arguments
yab list | devices             list devices on the configured servers
yab attach <device> | --all    attach (requires elevation)
yab detach <device> | --all    detach (requires elevation)
yab status                     servers, reachability and attached ports
yab doctor                     usbip, driver, elevation, Secure Boot, agents
yab install | uninstall        copy to %ProgramFiles%, ACL, logon task
```

`yab doctor` only reports; it never changes test signing, Secure Boot or the
driver.

## Building and checking

Requires Go 1.27 and, on Windows, Windows PowerShell 5.1. The project builds
with `CGO_ENABLED=0`. The only third-party dependencies are `fyne.io/systray`
and `golang.org/x/sys`, both confined to the Windows `yab` build (plus
`godbus/dbus` as an indirect Linux-only requirement of systray).

```powershell
# Cross-build dist/yab-windows-amd64.exe, dist/yabd-linux-arm64,
# dist/yabd-linux-armv7 (version metadata is stamped from git).
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/build.ps1

# Full local gate: gofmt -l, go vet, go test, go build, then the cross-builds.
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/check.ps1
```

`go test -race` is intentionally excluded from the local gate: the race
detector needs cgo/gcc, which this project does not otherwise require. CI runs
it on Ubuntu only.

## License

MIT — see `LICENSE`. Runtime tooling (`usbip`/`usbipd`/`usbip-win2`) is invoked
as separate programs and is not linked; see `THIRD_PARTY_NOTICES.md`.
