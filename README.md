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
internal/sdnotify   systemd sd_notify (READY/WATCHDOG/STATUS), stdlib only
internal/version    build-time version metadata (set via -ldflags -X)
deploy/pi           systemd units, modprobe blacklist and udev rule
scripts/build.ps1   cross-build dist/ artefacts for all three targets
scripts/check.ps1   local validation gate (gofmt, vet, test, build, cross-build)
```

Ports: the management API listens on **3241**. USB/IP uses **3240** and is
deliberately not configurable.

## Building and checking

Requires Go 1.27 and, on Windows, Windows PowerShell 5.1. The project builds
with `CGO_ENABLED=0` and depends only on the Go standard library (the sole
third-party module, `fyne.io/systray`, is added later and is Windows-only).

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
