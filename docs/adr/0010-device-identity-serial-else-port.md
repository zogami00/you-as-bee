# 10. Device identity: serial when present, else port path

- Status: Accepted
- Date: 2026-10-05

## Context

USB bus ids (`1-1.4`) change when a device is re-plugged into a different port,
and `devnum` changes on every re-enumeration. Configuration and the client must
refer to a device by something stable, and the agent must not export the wrong
device when two identical dongles are attached.

## Decision

Identify a device by **VID:PID:serial** when it reports a serial number, and by
**VID:PID@busid** otherwise (`internal/identity.Key`). A pin matches on VID/PID
always, and optionally narrows with an exact `serial` or an exact `port`
(bus path). If a pin matches more than one attached device it is **ambiguous**:
it is reported and never guessed.

## Consequences

- Serial-numbered devices keep a stable identity across ports and re-plugs.
- Devices without a serial fall back to the port path, which is stable only
  while they stay in the same socket; the operator should use a fixed port for
  such devices.
- Two identical serial-less dongles cannot be distinguished by a VID/PID-only
  pin; the `port` selector is the fix, and without it the device is reported
  ambiguous rather than silently mis-exported.
- The Windows client always uses the bus id the agent reports for the current
  generation, never a cached one, so it follows re-enumeration correctly.
