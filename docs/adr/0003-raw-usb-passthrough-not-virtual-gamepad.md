# 3. Raw USB passthrough rather than a virtual gamepad

- Status: Accepted
- Date: 2026-10-05

## Context

Controllers must reach Windows games. One approach is to read the controller on
the Pi and re-present it on Windows as a virtual gamepad (for example through
ViGEmBus). That requires understanding every controller protocol, adds latency,
and the commonly used Windows emulation shim, ViGEmBus, was archived in
November 2023 and is no longer maintained.

The alternative is to move the USB device itself to Windows, so Windows uses its
own native driver and the game sees exactly what it would see if the dongle were
plugged into the PC.

## Decision

Export the **raw USB device** over USB/IP and let Windows enumerate it as a real
local device. Do not emulate a gamepad. ViGEmBus-style virtual gamepads are
explicitly a fallback only if a device cannot be passed through (for example a
device with no workable Windows driver), and are not part of this milestone.

## Consequences

- Native Windows drivers and all device functions (audio, HID, vendor
  protocols) work, not just the gamepad subset.
- Low latency: no per-input parsing and re-encoding.
- The Pi must hand the device to `usbip-host` and keep it out of local drivers
  (see ADR 8).
- A device without a Windows driver still will not work; that is a fallback
  case, not the default.
