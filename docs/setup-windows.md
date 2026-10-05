# Setting up the Windows PC

The Windows client (`yab`) uses `usbip-win2` to attach devices exported by the
Pi so they appear as real local USB devices.

## 1. Install usbip-win2 and its driver

Install a `usbip-win2` release (the upstream project ships `usbip.exe` and the
`vhci` driver) and make sure `usbip.exe` is reachable. `yab` looks for it in
this order:

1. `usbip_path` from `client.json`;
2. the registry entry written by the installer;
3. `%ProgramFiles%\USBip\usbip.exe`;
4. `PATH`.

Verify from an elevated prompt:

```powershell
usbip version
usbip port
```

Pin the release you install and do not let it float. `install.ps1` can verify a
SHA-256 for a release archive if you supply one:

```powershell
.\install.ps1 -UsbipArchive .\usbip-win2.zip -UsbipSha256 <sha256>
```

## 2. Driver signing, Secure Boot and test-signing

This is the biggest Windows-side trade-off and you must make it knowingly.

- If the `usbip-win2` `vhci` driver is **not signed** in a way Windows trusts,
  Windows loads it only while **test-signing mode** is enabled. Without it the
  driver appears in Device Manager with **problem code 52** ("Windows cannot
  verify the digital signature").
- Enabling test-signing with `bcdedit /set testsigning on` disables driver
  signature enforcement **machine-wide** and normally requires **Secure Boot
  off**. Any kernel driver can then load.
- **Kernel anti-cheat refuses to run in test-signing mode.** Easy Anti-Cheat,
  BattlEye, Riot Vanguard, and similar services detect test-signing and will
  refuse to start or will terminate the game. If you game on this machine, this
  matters directly.

Prefer a **signed** `usbip-win2` release and leave test-signing and Secure Boot
alone. If you must enable test-signing, use a dedicated machine, and turn it
back off afterwards. `yab doctor` and `install.ps1` **only report** the state
they find; neither ever changes test-signing or Secure Boot.

`yab doctor` reports the Code 52 case as `driver signed: NO (problem code 52)`.

## 3. Run the installer

From an elevated PowerShell:

```powershell
cd deploy\windows
.\install.ps1 -PiHost raspberrypi.local -Token <the 64-hex token printed by provision.sh>
```

`install.ps1`:

- reports whether `usbip.exe` was found and its version;
- optionally verifies a `usbip-win2` archive SHA-256;
- **reports** test-signing and Secure Boot state and changes neither;
- copies `yab.exe` to `%ProgramFiles%\you-as-bee\yab.exe`;
- writes `%ProgramData%\you-as-bee\client.json` from `client.example.json`;
- restricts `%ProgramData%\you-as-bee` to Administrators and SYSTEM;
- registers a highest-privilege logon Scheduled Task running `yab.exe tray`.

It supports `-WhatIf` (safe without elevation) and is idempotent. If you omit
`-PiHost`/`-Token`, edit the config afterwards.

## 4. Configuration

See [configuration.md](configuration.md). At minimum set `servers[0].host` and
`servers[0].token` and confirm the `auto_attach` pin names match the agent.

## 5. Firewall

No Windows **inbound** rule is needed: the client makes outbound connections to
the Pi on 3240 (USB/IP) and 3241 (API). Outbound traffic is allowed by default.
The restriction that matters is on the Pi - the nftables rule limits inbound
3240/3241 to the configured `allowed_clients`. Make sure the PC's address is
inside that list.

## 6. Verify

```powershell
yab doctor        # usbip, driver, elevation, test signing, Secure Boot, servers
yab list          # devices and their state on each server
yab status        # API reachability, token validity, attached vhci ports
yab attach --all  # or: yab attach xbox-wireless  (requires elevation)
```

Then check Windows actually sees the device:

- **Device Manager** should show the Xbox/Bluetooth hardware under its normal
  category once attached.
- **`joy.cpl`** (Run > `joy.cpl`) should list the Xbox controller and respond
  when you press buttons.

The tray task runs at logon with highest privileges, because attaching a device
to a vhci port requires administrator rights. Run the full
[hardware validation checklist](hardware-validation.md).
