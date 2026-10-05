# 2. Orchestrate the external usbip tooling rather than reimplement USB/IP

- Status: Accepted
- Date: 2026-10-05

## Context

USB/IP is a mature protocol with a kernel half (`usbip-host` on Linux) and
user-space tooling (`usbip`, `usbipd`, and `usbip-win2` on Windows). We need to
export real USB devices over the LAN. Writing our own USB/IP stack would mean
reimplementing a documented wire protocol and a kernel driver interface, with a
large correctness and security surface, for no functional gain.

## Decision

Use the in-kernel `usbip-host` driver plus the stock `usbip`/`usbipd` tools on
the Pi and `usbip-win2` on Windows. you-as-bee supplies the parts those tools
lack: stable device pinning, a reconcile loop with backoff and quarantine, a
management API, and an auto-attaching Windows supervisor. Every external tool is
invoked as a separate process through `internal/execx`; nothing is shell-escaped
or linked.

## Consequences

- We inherit the tools' behaviour and bugs, including their lack of
  authentication on the data plane (see ADR 6 and `docs/security.md`).
- No USB/IP protocol code to maintain; our surface is the orchestration and the
  management API.
- Deployment must install the external tools, which `provision.sh` handles.
- The tools remain separate programs, so the licensing story is simple (see
  `THIRD_PARTY_NOTICES.md`).
