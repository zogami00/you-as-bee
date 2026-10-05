# Setting up the Raspberry Pi

The Pi attaches a Bluetooth dongle and an Xbox wireless dongle to a powered USB
hub and exports them over the LAN. The Windows PC then sees them as real local
USB devices.

## Why the Pi 4B is preferred

- **Pi 4B** - USB 3.0 ports, a real power budget, and enough CPU to run the
  agent and the Wi-Fi stack comfortably. This is the primary target.
- **Pi Zero 2 W** - a secondary target. It has a **single micro-USB OTG data
  port** and much tighter power limits. See the caveats below before choosing
  it.

## Hardware wiring

```
  [powered USB hub]
    |-- Bluetooth dongle
    |-- Xbox wireless dongle
    |
  uplink cable --> [data USB port on the Pi]

  [separate power supply] --> [PWR port on the Pi]
```

Rules:

- The hub must be **powered**. Both dongles draw real current (the Xbox adapter
  in particular), and the Zero 2 W cannot supply them.
- Never power the Pi through the hub's uplink and never backfeed the Pi's PWR
  port from the hub. The Pi gets its own supply.
- Use the hub's uplink cable to the Pi's **data** USB port.

### Pi Zero 2 W caveats

- Only one micro-USB port carries data (the OTG port); the other is power-only.
  If you need wired Ethernet and a hub at the same time you need a USB **OTG
  hub**, or a hub with an integrated Ethernet adapter.
- With no wired Ethernet the Zero 2 W runs over Wi-Fi. A Bluetooth dongle and
  Wi-Fi share the same radio spectrum; Bluetooth audio and controllers can be
  unstable over Wi-Fi (see [troubleshooting.md](troubleshooting.md)). Prefer
  the Pi 4B, or use a USB Ethernet adapter, if that matters.
- Power the Zero 2 W from a good 5V/2.5A supply; a brown-out shows up as
  random USB disconnects.

## Flash the operating system

Use Raspberry Pi OS **Bookworm** (the installer expects a Debian bookworm-era
release and warns otherwise), enable SSH, set a hostname and a user, and boot.
The agent needs root for sysfs writes, so `provision.sh` is run with `sudo`.

## Provision

On Windows, build the cross-compiled agent and deploy it:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\build.ps1
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\deploy-pi.ps1 `
  -HostName raspberrypi.local -ClientCidr 192.168.1.0/24
```

`deploy-pi.ps1` copies `dist\yabd-linux-arm64` (or `-Arch armv7`) and the
`deploy\pi` support files to the Pi and runs `provision.sh` over SSH with sudo.
It embeds no credentials; `ssh`/`scp` use your existing keys or prompt.

If you prefer to run it on the Pi yourself, copy the whole `deploy/pi`
directory (including the `yabd` binary) and run:

```bash
sudo ./provision.sh --binary ./yabd-linux-arm64 --client-cidr 192.168.1.0/24
```

### What provisioning does

1. **Preflight** - root, Raspberry Pi OS bookworm, `modprobe usbip-host`
   succeeds, `python3` present.
2. Installs the `usbip` package (and `nftables` when the firewall is enabled).
3. Installs `/etc/modules-load.d/yab.conf` (loads `usbip-host` at boot), the
   modprobe blacklist `/etc/modprobe.d/yab-modprobe.conf`, and the udev rule
   `/etc/udev/rules.d/90-you-as-bee.rules`.
4. Installs the agent to `/usr/local/bin/yabd` and both systemd units to
   `/etc/systemd/system/`.
5. Creates `/etc/you-as-bee/token` (64 hex chars from `/dev/urandom`, mode
   `0600`) and `/etc/you-as-bee/agent.json` derived from the example. It
   **prints the token once**; copy it to the Windows client now.
6. Applies the nftables rule allowing 3240/3241 only from `allowed_clients`.
7. Enables and starts `usbipd.service` and `yabd.service`. If the agent
   binary, `agent.json`, or `yabd.service` changed, an already-active
   `yabd.service` is restarted so the running daemon picks the change up.

It is idempotent: run it twice and the second run reports `provision: no
changes`, without reprinting the token.

If you provisioned the Pi **before** the loopback guarantee was added (an
`agent.json` whose `allowed_clients` omits `127.0.0.0/8`, and/or an nftables
rule whose final drop also matched `lo`), re-run provisioning once to migrate
it:

```bash
sudo ./provision.sh            # or: scripts\deploy-pi.ps1 -HostName ...
```

It rewrites `agent.json` to include `127.0.0.0/8` and replaces the nftables
rule with one that accepts loopback independently. The agent reads its config
once at startup (there is no reload and no SIGHUP handling), so when
provisioning rewrites `agent.json`, `yabd.service`, or the binary it also
restarts an active `yabd.service`; the loopback allowlist and the
`ConfigurationDirectory` removal take effect immediately, without a manual
restart. This first run reports changes; a second run changes nothing and
reports `provision: no changes` (and does not bounce the service).

If the service was not running when you re-ran provisioning, it is started
rather than restarted. If for any reason the new allowlist is not in effect
afterwards, apply it by hand:

```bash
sudo systemctl restart yabd
```

### Why btusb is blacklisted

`yab-modprobe.conf` blacklists `btusb` and `xone` so they do not claim the
dongles we want to export. This is safe on the Pi 4B and Zero 2 W because the
**onboard** Bluetooth radio is wired to the SoC over the UART and driven by
`hci_uart`, not by `btusb` over USB. The onboard adapter keeps working.
`yabd doctor` checks this (`hci0` must be on `hci_uart`).

## Configure the dongles

`provision.sh` copies the example's device list. Edit
`/etc/you-as-bee/agent.json` to match your dongles and restart:

```bash
sudo nano /etc/you-as-bee/agent.json   # or use: yabd pin --name NAME <busid>
sudo systemctl restart yabd
```

Find the ids with:

```bash
yabd list
```

## Verify

```bash
systemctl status usbipd.service yabd.service
yabd doctor                  # root, modules, binaries, usbipd, blacklist, udev, hci_uart
yabd status                  # info plus the state of every pin
yabd list                    # raw sysfs enumeration
journalctl -u yabd -f        # live agent log
```

`yabd doctor` should end with `all checks passed`. Then run the
[hardware validation checklist](hardware-validation.md) from the Windows side.
