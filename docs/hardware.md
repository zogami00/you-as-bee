# Hardware

## Requirements

- Raspberry Pi **4B** (primary) or **Zero 2 W** (secondary) running Raspberry
  Pi OS **Bookworm**.
- A **powered USB hub** with at least two free ports for the dongles and one
  uplink to the Pi's data port.
- A separate, adequate power supply for the Pi.
- A Bluetooth USB dongle and/or an Xbox wireless dongle.
- A Windows 10/11 PC on the same LAN with `usbip-win2` installed.
- Wired Ethernet is strongly preferred; Wi-Fi works but see the caveats.

## Bluetooth dongles

The agent exports the dongle as **raw USB**, so it does not care which chipset
it is. What matters:

- The Windows PC must have a Bluetooth stack/driver that recognises the dongle
  once it is attached. Common, well-supported chipsets are the easiest.
- The shipped example uses `0a12:0001`, the id reported by most inexpensive
  CSR8510-based "CSR 4.0" dongles. Many of these are functionally identical and
  share that id; if they are not two at once, VID/PID matching is fine.
- Realtek RTL8761B dongles (`0bda:8771`) are a broadly supported BT 5 class if
  you want a newer radio. Set the VID/PID in `agent.json` accordingly.
- Avoid dongles that present as a composite device with a mass-storage or
  audio function you also need locally; the whole device is moved to
  `usbip-host`.

Because `btusb` is blacklisted (see [setup-pi.md](setup-pi.md)), the dongle is
never used locally on the Pi. The Pi's onboard Bluetooth keeps working through
`hci_uart`.

## Xbox wireless dongles

- `045e:02e6` - Xbox Wireless Adapter for Windows (the larger, older model).
  Used by the shipped example and udev rule.
- `045e:02fe` - the newer, smaller Xbox Wireless Adapter.
- Xbox 360 wireless receivers use different ids again (`045e:028f` /
  `045e:0291`) and are a separate device class.

Set the correct VID/PID in `agent.json`. The Xbox adapter needs a driver on
Windows; when the device is attached over USB/IP it enumerates as if plugged in
locally.

## Power notes

- The hub must be **powered**. An unpowered hub will brown out the dongles and
  produce random disconnects.
- The Xbox adapter draws a meaningful amount of current; budget per-port
  current, not just total.
- The Pi 4B's own USB ports have a limited power budget. Do not also power the
  Pi from the hub; give it its own supply.
- Symptoms of a power problem: devices re-enumerate repeatedly, Bluetooth audio
  stutters, controllers drop. Check `dmesg` for USB resets and move to a better
  hub before blaming the software.

## Pi Zero 2 W caveats

- **One data port.** The Zero 2 W has a single micro-USB OTG data port; the
  other micro-USB is power-only. You cannot connect a hub and a USB Ethernet
  adapter without an OTG hub or a hub with integrated Ethernet.
- **Wi-Fi and Bluetooth share the 2.4 GHz radio.** A USB Bluetooth dongle used
  over Wi-Fi can be unstable, especially for audio. Wired Ethernet is the fix.
- **Power budget.** The Zero 2 W has little headroom; the powered hub is not
  optional on this board.
- It works, but the Pi 4B is the better experience for gaming and for
  Bluetooth stability.

## LAN

USB/IP moves real USB traffic over TCP. For controllers that is low bandwidth
but latency-sensitive. Use a wired connection for the Pi where possible and
keep the PC and Pi on the same switch/VLAN.
