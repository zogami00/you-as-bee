# you-as-bee

USB-over-LAN device sharing ? a VirtualHere clone.

A Raspberry Pi exports USB devices (a Bluetooth dongle and an Xbox wireless
dongle attached to a powered hub) over the LAN, so a Windows PC sees them as
real local USB devices and can use controllers connected through the Pi.

- Pi side: `yabd` ? a systemd service that pins, exports and recovers devices.
- Windows side: `yab` ? a tray app and CLI that auto-attaches them.
- Built on the existing Linux `usbip`/`usbipd` server and `usbip-win2`.

See `docs/` for architecture, setup and the hardware validation checklist.
