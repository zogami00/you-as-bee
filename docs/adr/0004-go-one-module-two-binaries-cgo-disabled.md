# 4. Go, one module, two binaries, CGO_ENABLED=0

- Status: Accepted
- Date: 2026-10-05

## Context

We need a Linux daemon for the Pi and a Windows tray application for the PC,
sharing config, protocol and device-identity logic. Both targets are small and
network-bound. We want a single language, a single repository, and cross-builds
from the Windows development machine without a C toolchain.

## Decision

Use Go in **one module**, `github.com/zogami00/you-as-bee`, producing two
binaries:

- `cmd/yabd` - the Pi agent, cross-built for `linux/arm64` and `linux/arm`
  (`GOARM=7`);
- `cmd/yab` - the Windows client, built for `windows/amd64`.

Build with **`CGO_ENABLED=0`** for every target. Shared internal packages
(`config`, `proto`, `api`, `identity`, `sysfs`, `execx`, ...) keep the two
binaries consistent.

## Consequences

- A single `go build` toolchain cross-compiles all three targets; no gcc, no
  cgo. `scripts/build.ps1` and `scripts/check.ps1` rely on this.
- The race detector needs cgo, so `go test -race` runs on Ubuntu CI only and is
  deliberately excluded from the local Windows gate.
- Platform-specific behaviour is isolated behind small, injectable interfaces
  and build tags (`bind_linux.go` / `bind_other.go`, `tray_windows.go` /
  `tray_other.go`), which keeps the logic testable on Windows with fakes.
- Two binaries share one version scheme via `internal/version` and `-ldflags`.
