# 8. Driver-unbind strategy and the `btusb` blacklist

- Status: Accepted
- Date: 2026-10-05

## Context

To export a USB device with `usbip-host`, the device must first be released by
whatever kernel driver currently owns it (for example `btusb` for a Bluetooth
dongle, `xone`/`xpad` for an Xbox adapter). A driver can grab a device the
moment it enumerates, so doing this only inside the agent creates a race and
leaves the device unusable locally in the meantime.

The Raspberry Pi 4B and Zero 2 W wire their **onboard** Bluetooth radio to the
SoC over the UART, driven by `hci_uart`, not by `btusb` over USB. That means
blacklisting `btusb` does not disable onboard Bluetooth.

## Decision

Two complementary mechanisms:

1. A modprobe blacklist (`/etc/modprobe.d/yab-modprobe.conf`) for `btusb` and
   `xone`, so those drivers never claim the dongles we intend to export. A
   `softdep usbip-host pre: usbip-core` ensures the module stack loads in order.
2. At bind time, the sequence in `internal/usbiphost` releases the current
   driver (`unbind`), binds `usbip-host`, then verifies the driver symlink,
   `usbip_status` and the device generation. Rollback re-probes the original
   driver on any failure.

`yabd doctor` verifies the premise at runtime: `hci0` must be on `hci_uart`.

## Consequences

- The blacklist is safe on the supported boards and documented as such; before
  reusing this project on different hardware, verify onboard Bluetooth is not
  `btusb`, or onboard Bluetooth will stop working.
- Devices not on the pin list can still be claimed by their normal drivers; the
  blacklist is global but only affects `btusb`/`xone`.
- The bind sequence never touches a device a client is attached to unless
  `force` is explicit, and it rolls back cleanly on failure, so a failed export
  does not strand the device.
