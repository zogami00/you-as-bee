# Troubleshooting

## A device disappeared from the Pi

If a pinned device is bound to `usbip-host` but nothing is exported, or you
removed the agent while a device was still bound, the device may not be visible
locally. Return it to its original driver:

```bash
# find devices currently on usbip-host
for l in /sys/bus/usb/devices/*/driver; do
  [ -e "$l" ] || continue
  [ "$(basename "$(readlink -f "$l")")" = usbip-host ] && basename "$(dirname "$l")"
done

# release one (replace 1-1.4)
echo 1-1.4 | sudo tee /sys/bus/usb/drivers/usbip-host/unbind
echo 1-1.4 | sudo tee /sys/bus/usb/drivers_probe
```

`deploy/pi/uninstall.sh` does this for every bound device automatically. A
reboot also recovers it. Binding is non-destructive: the original driver claims
the device again when it is unbound.

If the device stays on `usbip-host` because a client is attached, detach it on
the Windows side first (`yab detach --all`).

## Driver re-claim loop on the Windows side

If the device attaches then immediately detaches and re-attaches:

- Another driver on Windows may be racing for the device. Attach it once with
  `yab attach <device>` and watch Device Manager for the driver that loads.
- Check `yab status` for a stale vhci port from a previous session pointing at
  an old host address. The supervisor only detaches stale ports whose host
  matches the server (bus ids repeat across Pis), so a port for an old host
  address may linger; clear it with `usbip detach -p <port>`.
- If two servers are configured and attaching one tears down the other, they
  share a bus id: the supervisor scopes detach to the owning server, so verify
  each server's `host` resolves to the address `usbip port` prints.

## Bluetooth is unstable over Wi-Fi

Bluetooth and Wi-Fi share the 2.4 GHz radio. On a Pi Zero 2 W (Wi-Fi only by
default) this shows up as dropouts, stutter or re-enumeration.

- Move the Pi to **wired Ethernet**. This is the reliable fix.
- Separate the Wi-Fi and Bluetooth radios physically: a USB extension cable for
  the dongle so it is not sitting on top of the Wi-Fi antenna.
- Use a good powered hub; brown-outs look exactly like radio trouble.
- Confirm the dongle is exported on `usbip-host` and not being reset:

  ```bash
  dmesg | tail -50
  yabd status
  ```

## Code 52: Windows blocked the driver

`device manager` shows the USB/IP device with **Code 52** ("Windows cannot
verify the digital signature"). The `usbip-win2` vhci driver is unsigned (or
not trusted) and driver signature enforcement is on.

- Best: install a **signed** `usbip-win2` release.
- Otherwise enable test-signing per the upstream `usbip-win2` documentation.
  This disables signature enforcement machine-wide and requires Secure Boot
  off; see [setup-windows.md](setup-windows.md).
- `yab doctor` reports `driver signed: NO (problem code 52)` in this case.

## Secure Boot

- Test-signing normally needs **Secure Boot disabled**. Check the state with
  `yab doctor` (it only reports; it never changes anything).
- Changing Secure Boot or test-signing is a manual, deliberate step. Do not
  automate it, and remember kernel anti-cheat will refuse to run while
  test-signing is on.

## Firewall

Symptoms: the API hangs or returns `403`, or `usbip attach` times out, while
`ping` works.

On the Pi, check the nftables rule:

```bash
sudo nft list table inet yab
sudo nft list ruleset | grep -E '3240|3241'
```

- Both 3240 and 3241 must be reachable **and** the PC's address must be inside
  `allowed_clients` in `/etc/you-as-bee/agent.json`.
- If you tightened `allowed_clients` after provisioning, re-run
  `sudo ./provision.sh --client-cidr <cidr>` to regenerate the rule.
- On Windows, no inbound rule is needed; outbound to 3240/3241 must not be
  blocked by a third-party firewall.

## Port conflicts

- **3240 (USB/IP)** is fixed. If something else on the Pi already listens on
  3240, `usbipd` fails to start. Check with
  `sudo ss -ltnp | grep 3240` and stop the other service.
- **3241 (management API)** is configurable via `listen` in `agent.json`. If it
  is taken, `yabd` fails at startup; change `listen` (for example to
  `0.0.0.0:13241`) and update `api_port` in the client config to match.
- A device exported but not attachable can also mean `usbipd` is not running:
  `systemctl status usbipd` and `yabd status` (`usbipd:` line).

## Agent will not start

```bash
journalctl -u yabd -n 100 --no-pager
yabd run --config /etc/you-as-bee/agent.json   # run in the foreground
```

Common causes:

- `/etc/you-as-bee/token` missing or empty (re-run `provision.sh`; it creates it
  once and does not overwrite an existing one).
- `agent.json` fails strict validation: an unknown field, a comment, a bare
  number where a duration string belongs, a bad CIDR, or `schema_version`
  other than `1`. The error names the field.
