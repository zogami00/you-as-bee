# you-as-bee

USB-over-LAN device sharing - a VirtualHere clone.

A Raspberry Pi exports USB devices (a Bluetooth dongle and an Xbox wireless
dongle attached to a powered hub) over the LAN, so a Windows PC sees them as
real local USB devices and can use controllers connected through the Pi.

- Pi side: `yabd` - a systemd service that pins, exports and recovers devices.
- Windows side: `yab` - a tray app and CLI that auto-attaches them.
- Built on the existing Linux `usbip`/`usbipd` server and `usbip-win2`.

## Quick start

1. [Set up the Pi](docs/setup-pi.md) - hardware wiring, flashing, provisioning.
2. [Set up the Windows PC](docs/setup-windows.md) - `usbip-win2`, the driver
   signing trade-off, `install.ps1`, config.
3. Run the [hardware validation checklist](docs/hardware-validation.md) - the
   items the code cannot prove on its own.

## Install

**Pi.** From the released bundle in one line (the repository is private, so a
token with `repo` scope is required; see [setup-pi.md](docs/setup-pi.md)):

```bash
curl -fsSL -H "Authorization: Bearer $GITHUB_TOKEN" \
  https://github.com/zogami00/you-as-bee/releases/download/v1.0.0/install.sh \
  | sudo bash -s -- --release v1.0.0 --token "$GITHUB_TOKEN"
```

Or from a checkout: `scripts\deploy-pi.ps1` over SSH, or copy `deploy/pi` and
run `sudo ./provision.sh`.

**Windows.** From an elevated PowerShell, with both `yab.exe` and `yabw.exe`
next to the script (a packaged bundle) or built into `dist\`:

```powershell
cd deploy\windows
.\install.ps1 -PiHost raspberrypi.local -Token <the 64-hex token printed by provision.sh>
```

`install.ps1` also accepts `-BundleDir`, `-BundleUrl`/`-BundleToken` and the
legacy `-SourcePath`, and supports `-WhatIf` in every mode. It registers the
logon task against **`yabw.exe`**, the windowless (GUI-subsystem) build of
`cmd/yab`, so the tray starts at logon without a console window; `yab.exe`
stays the console CLI. See [setup-windows.md](docs/setup-windows.md).

Read [security.md](docs/security.md) before exposing anything: the management
API has a token and an allowlist but **no TLS**, and USB/IP on 3240 is
unauthenticated.

## What it is not

- It is not a secure remote-access product. It is a home-LAN tool; see the
  honest trust model in [security.md](docs/security.md).
- It does not emulate a gamepad. It passes the real USB device through, so
  Windows uses its own drivers.
- It does not run on Windows as a service. The client is a per-user,
  highest-privilege tray task.

## Limitations

- **Bluetooth over Wi-Fi.** Bluetooth and Wi-Fi share the 2.4 GHz radio. On a
  Pi Zero 2 W over Wi-Fi, audio and controllers can be unstable. Wired Ethernet
  is the reliable fix. See [troubleshooting.md](docs/troubleshooting.md).
- **Driver signing and anti-cheat.** `usbip-win2`'s `vhci` driver may need
  Windows **test-signing** mode, which disables driver signature enforcement
  machine-wide and requires Secure Boot off. **Kernel anti-cheat refuses to run
  in test-signing mode.** Prefer a signed release and leave test-signing alone.
  See [setup-windows.md](docs/setup-windows.md).
- **No TLS, unauthenticated USB/IP.** The bearer token is sent in cleartext and
  anyone who can reach TCP 3240 can attach to an exported device. The nftables
  allowlist is a fence, not a lock.
- **Pi Zero 2 W caveats.** A single USB data port (an OTG hub or Ethernet hub is
  needed for both a hub and Ethernet) and a tight power budget. The Pi 4B is the
  preferred board.

## Layout

```
cmd/yabd            Raspberry Pi agent (Linux) - management API on :3241
cmd/yab             Windows client - tray + CLI
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
deploy/pi           provisioning/uninstall scripts, curl bootstrap (install.sh),
                    systemd units, modprobe blacklist, udev rule, agent config
deploy/windows      install/uninstall PowerShell, client example config
scripts/build.ps1   cross-build dist/ artefacts (yab, yabw, both yabd targets)
scripts/deploy-pi.ps1  scp + ssh provisioning from Windows
scripts/release.ps1  cut a GitHub release and attach the built artefacts
scripts/check.ps1   local validation gate (gofmt, vet, test, build, cross-build)
docs/               architecture, configuration, API, security and setup guides
docs/adr/           architecture decision records
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

`yab tray` also serves a **local browser UI** (loopback only, on by default).
Open it from the tray menu: **Open web UI**. It shows each pin's state with
Attach/Detach buttons, server reachability and the recent client log, and the
browser never receives the Pi token. See
[setup-windows.md](docs/setup-windows.md).

`yab doctor` only reports; it never changes test signing, Secure Boot or the
driver.

## Building and checking

Requires Go 1.27 and, on Windows, Windows PowerShell 5.1. The project builds
with `CGO_ENABLED=0`. The only third-party dependencies are `fyne.io/systray`
and `golang.org/x/sys`, both confined to the Windows `yab` build (plus
`godbus/dbus` as an indirect Linux-only requirement of systray).

```powershell
# Cross-build dist/yab-windows-amd64.exe (console CLI), the windowless
# dist/yabw-windows-amd64.exe (tray), dist/yabd-linux-arm64 and
# dist/yabd-linux-armv7 (version metadata is stamped from git).
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/build.ps1

# Full local gate: gofmt -l, go vet, go test, go build, then the cross-builds.
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/check.ps1
```

`go test -race` is intentionally excluded from the local gate: the race
detector needs cgo/gcc, which this project does not otherwise require. CI runs
it on Ubuntu only.

## License

MIT - see `LICENSE`. Runtime tooling (`usbip`/`usbipd`/`usbip-win2`) is invoked
as separate programs and is not linked; see `THIRD_PARTY_NOTICES.md`.
