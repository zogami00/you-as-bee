# Hardware validation checklist

The unit tests prove the logic with fakes. They cannot prove that a real dongle
binds, that a real controller works, or that Bluetooth survives a Wi-Fi soak.
This is the list a human runs on real hardware. Work through it on the Pi and
the Windows PC, and note anything that fails.

Legend: `PI$` is a shell on the Pi, `PC>` is an elevated PowerShell on Windows.

## 1. Agent is running and READY

```bash
PI$ systemctl is-active yabd          # active
PI$ systemctl show -p SubState yabd   # running
PI$ systemctl show -p StatusText yabd  # SERVING on 0.0.0.0:3241
PI$ yabd doctor                        # all checks passed
```

- [ ] `yabd` is `active (running)` and systemd's `StatusText` reflects serving.
- [ ] `yabd doctor` prints `all checks passed` (root, modules, binaries, usbipd
      on 3240, blacklist, udev rule, `hci0` on `hci_uart`).

## 2. Both dongles are exported

```bash
PI$ yabd list
PI$ yabd status
```

- [ ] `yabd list` shows the Bluetooth dongle with driver `usbip-host`.
- [ ] `yabd list` shows the Xbox dongle with driver `usbip-host`.
- [ ] `yabd status` shows the `xbox-wireless` pin in state `exported` (it is
      `always`) and the `bluetooth` pin `unexported` (it is `on_demand`) until a
      client asks for it.

## 3. The token is actually required

From the Windows PC (replace the token):

```powershell
PC> curl.exe -s -o NUL -w "%{http_code}`n" http://raspberrypi.local:3241/v1/info
PC> curl.exe -s -o NUL -w "%{http_code}`n" -H "Authorization: Bearer wrong" http://raspberrypi.local:3241/v1/info
PC> curl.exe -s -H "Authorization: Bearer <real token>" http://raspberrypi.local:3241/v1/info
```

- [ ] No token returns `401`.
- [ ] A wrong token returns `401`.
- [ ] The real token returns `200` and a JSON body.
- [ ] A host outside `allowed_clients` gets `403` (test from another subnet if
      you can).

## 4. Watchdog restarts a wedged agent

```bash
PI$ systemctl show -p NRestarts yabd
PI$ sudo kill -STOP "$(systemctl show -p MainPID --value yabd)"
# wait at least 30 seconds
PI$ systemctl status yabd
```

- [ ] After roughly 30-40 seconds systemd kills the stopped process and
      restarts it, showing `active (running)`.
- [ ] `NRestarts` increased by one.

(The unit sets `WatchdogSec=30`; the agent resets the timer every 10 seconds.
This proves a hung agent does not stay hung.)

## 5. Unplug and re-plug re-exports

With `yabd status` open in a second terminal:

- [ ] Unplug the Bluetooth dongle: the pin moves to `absent`.
- [ ] Re-plug it: the pin returns to `exported` (or `unexported` for an
      on-demand pin until attached), with a new bus id if the port changed.
- [ ] Repeat for the Xbox dongle.

## 6. Attach from Windows

```powershell
PC> yab doctor
PC> yab list
PC> yab attach --all
PC> yab status
```

- [ ] `yab doctor` finds `usbip.exe`, reports the driver, and both servers are
      reachable with a valid token.
- [ ] `yab attach --all` succeeds and `yab status` lists a local vhci port for
      each device.
- [ ] On the Pi, `yabd status` now shows those pins `in_use`.

## 7. Windows sees real USB devices

- [ ] Device Manager shows the Bluetooth radio and the Xbox wireless adapter
      each under their normal category (not "unknown device", not Code 52).
- [ ] `joy.cpl` lists the Xbox controller and reacts to button presses.

## 8. Controller works in a game

- [ ] Launch a game and confirm the controller drives it through the Pi.

## 9. 30-minute gameplay soak (Bluetooth stability)

This is the test that catches radio trouble the other checks miss.

- [ ] Play (or leave a controller connected) for **30 continuous minutes** over
      Bluetooth, and keep using the Xbox controller too.
- [ ] No disconnects, no re-enumeration, no audio dropouts.
- [ ] On the PC, `yab status` still shows the same vhci ports at the end.
- [ ] On the Pi, `dmesg` shows no repeated USB reset or `-71`/`-110` errors.

If this fails on a Zero 2 W over Wi-Fi, move to wired Ethernet and repeat; see
[troubleshooting.md](troubleshooting.md).

## 10. Network drop recovers

```text
# pull the Pi's Ethernet cable (or disable the Pi's Wi-Fi) for 10 seconds,
# then restore it
```

- [ ] While down, `yab status` on the PC reports the server unreachable and the
      supervisor backs off (client state `network_down`).
- [ ] Within a short time after restoring the link, the client re-attaches and
      `yab status` shows the ports again.
- [ ] On the Pi, the pins go back to `in_use`.

## 11. Reboot recovers

```bash
PI$ sudo reboot
# wait for the Pi to come back
```

- [ ] After reboot, `systemctl is-active yabd usbipd` are both `active`.
- [ ] `yabd status` shows the pins exported again.
- [ ] The Windows client re-attaches automatically without manual help.

## Sign-off

Record for each item: pass/fail, board (4B or Zero 2 W), connection (wired or
Wi-Fi), dongle models, and any log lines. Failures here are the input to
[troubleshooting.md](troubleshooting.md).
