# 9. Always-on export for pinned devices vs on-demand

- Status: Accepted
- Date: 2026-10-05

## Context

Some devices should be available the instant a client wants them (a controller
before a game starts); others should only leave the Pi when actually needed
(for example a Bluetooth dongle used occasionally for a headset). Exporting
everything eagerly keeps devices bound to `usbip-host` and reachable on 3240
even when nobody is using them, which widens the window in which an
unauthenticated LAN peer could attach.

## Decision

Give every pin a `mode`:

- **`always`** - export as soon as the device is present.
- **`on_demand`** (default) - export only when a client explicitly asks
  (`yabd export`, or the Windows supervisor before its first attach). Once
  exported, an on-demand device stays exported until it is unexported or
  disappears.

The reconcile loop treats `desired = mode == always || explicit`. Both modes
still retry with backoff and are subject to quarantine.

## Consequences

- Latency-sensitive, constantly-used devices use `always` (the example pins the
  Xbox adapter as `always`).
- Devices used occasionally use `on_demand`, keeping them off the USB/IP port
  most of the time (the example pins Bluetooth as `on_demand`).
- The Windows supervisor knows to call `export` for an on-demand device before
  attaching, so the operator does not have to.
- `on_demand` is only a smaller window, not a security control; an exported
  device is always reachable on unauthenticated 3240.
