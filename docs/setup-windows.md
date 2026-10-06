# Setting up the Windows PC

The Windows client (`yab`) uses `usbip-win2` to attach devices exported by the
Pi so they appear as real local USB devices.

## 1. Install usbip-win2 and its driver

Install a `usbip-win2` release **at least 0.9.8.0** (the upstream project ships
`usbip.exe` and the `vhci` driver) and make sure `usbip.exe` is reachable.
`--receive-mode`, which every attach passes, exists only from 0.9.8.0; v0.9.7.7
has `--once` but not `--receive-mode`, and attach against it fails permanently
with a generic `usbipwin: usbip exited N`. `yab doctor` reports the version it
finds and warns when it is older or unrecognised. `yab` looks for `usbip.exe` in
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

Or install directly from a released bundle, with no checkout:

```powershell
cd deploy\windows
.\install.ps1 -Release v1.0.0 -PiHost raspberrypi.local -Token <the 64-hex token>
```

`-Release` accepts a tag or `latest` and needs **no token**: the repository is
public, so the assets come from the plain release download URL. (`-BundleToken`
is only for a private fork.)

`install.ps1`:

- reports whether `usbip.exe` was found and its version;
- optionally verifies a `usbip-win2` archive SHA-256;
- **reports** test-signing and Secure Boot state and changes neither;
- copies **both** `yab.exe` and `yabw.exe` to `%ProgramFiles%\you-as-bee\`
  (stopping a running tray first so the locked binaries can be replaced);
- creates `%ProgramData%\you-as-bee` and restricts it to Administrators and
  SYSTEM **before** writing the token-bearing config;
- writes `%ProgramData%\you-as-bee\client.json` from `client.example.json`;
- registers a highest-privilege logon Scheduled Task running `yabw.exe tray`,
  with an unlimited execution-time limit and battery-friendly settings.

It supports `-WhatIf` (safe without elevation) and is idempotent: a second run
changes nothing and reports `Install complete: no changes were needed.` If you
omit `-PiHost`/`-Token`, edit the config afterwards.

### Why there are two binaries

`cmd/yab` is one program that is both the tray and the CLI. Compiled normally
it is a **console** binary, so starting the tray (`yab.exe tray`) opens a black
console window and closing that window kills the tray. `yabw.exe` is the **same
package built a second time** with `-ldflags "-H windowsgui"` (Go's GUI
subsystem), so Windows gives it no console at all. There is no code
duplication and nothing to keep in sync: it is the identical main package.

- `yab.exe` stays a console binary and is what you type into a prompt: `yab
  list`, `yab status`, `yab doctor`, `yab attach`, ...
- `yabw.exe` is the **tray only**. Because it has no console, its CLI
  subcommands have nowhere to print: `yabw.exe list` runs but produces no
  visible output. Launch it with no arguments (or `tray`) and use `yab.exe` for
  everything else.
- The logon task runs `yabw.exe tray`, so logon does not open a console for the
  tray itself. Because `yabw.exe` has no console, any console child it runs
  (`usbip.exe`) is started with `CREATE_NO_WINDOW` so it does not flash a
  console window on each reconcile tick or attach.

Because `yabw.exe` has no console, a **startup failure is silent**: if the tray
exits or fails to start at logon, nothing is shown. To see the error, run
`yab.exe tray` in a console (it is the same program), or set `"log_file"` in
`client.json` to a writable path (for example
`%ProgramData%\you-as-bee\yab.log`); the tray appends its log records there
once it has read the config, so a failure after config load is captured without
any new machinery.

To migrate an existing install whose task still runs `yab.exe`, re-run
`install.ps1 -Force` (the task name is unchanged, so a plain re-run keeps the
old action).

The Go subcommand `yab install` matches `install.ps1`: it copies **both**
`yab.exe` and `yabw.exe` into `%ProgramFiles%\you-as-bee\`, stops both process
names, and registers the logon task against `yabw.exe tray`. If `yabw.exe` is
not beside the `yab.exe` you run `yab install` from, it fails with a message
naming the missing file rather than registering a console-launching task.
`yab uninstall` stops and removes both.

### Installer source modes

`install.ps1` can get the binaries from four places, in this order of
preference:

1. **Local bundle** (default): `-BundleDir <dir>`, or the script's own
   directory when it already contains `yab.exe` and `yabw.exe`. This is what a
   packaged release looks like - no checkout, no `dist\` layout.
2. **GitHub release**: `-Release <tag>` (or `latest`). The public repository
   needs no token, so assets come from the plain release download URL
   (`https://github.com/<repo>/releases/download/<tag>/<asset>`). A private fork
   passes `-BundleToken <token>`, which switches to the authenticated GitHub API
   asset path; the token is sent only as a request header and is never written
   to disk.
3. **Generic download**: `-BundleUrl <base-url>` fetches `<base-url>/yab.exe`,
   `<base-url>/yabw.exe` and `<base-url>/client.example.json` to a temporary
   directory. For a private host pass `-BundleToken <token>`; the token is sent
   as `Authorization: Bearer <token>`. Every download must be non-empty and each
   binary must have a valid PE header (`MZ` plus a `PE\0\0` signature), so an
   HTML error page saved as `yab.exe` is rejected before anything reaches
   `%ProgramFiles%`.
4. **Legacy checkout layout**: `-SourcePath` (default
   `..\..\dist\yab-windows-amd64.exe`), with `yabw-windows-amd64.exe` expected
   beside it. This preserves the previous behaviour for a working copy.

`-WhatIf` works in all four modes. In release and download modes it still
fetches the bundle into a temporary directory (so the URL and the file headers
are validated) but changes nothing on the system; the temporary directory is
always removed.

`%ProgramData%\you-as-bee` is restricted to Administrators and SYSTEM because
`client.json` holds the bearer token. Every `yab` command reads that file, so
run them from an **elevated** prompt; unelevated they fail with
`yab: cannot read C:\ProgramData\you-as-bee\client.json; run from an elevated
prompt (the config holds the API token)`.

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
to a vhci port requires administrator rights. The task is registered with an
unlimited execution-time limit, starts on battery, and `StartWhenAvailable`, so
it is not silently killed after 72 hours.

## 7. The local browser UI

`yab tray` serves a small browser UI that shows each pin's state and offers
**Attach** and **Detach**, plus a server-reachability panel and the recent
client log records. Open it from the tray menu: **Open web UI**.

- The UI is **loopback only**. It binds `web_ui.listen` (default
  `127.0.0.1:0`, where `0` means the OS picks a free port) and refuses any
  non-loopback host, so it cannot be reached from another machine. There is no
  Windows firewall rule to add.
- A **one-time code** does the authentication. The tray mints a code, opens
  `http://127.0.0.1:<port>/ui/login/<code>` through `explorer.exe` (so the
  browser starts de-elevated even though the tray runs elevated) and the code is
  bound to that browser. It is single-use and expires after 60 seconds; the code
  leaves the URL as soon as it is redeemed. The code is a **path segment, not a
  query string**, on purpose: `explorer.exe` treats a URL containing `?` as a
  filesystem path, launches no browser and opens a folder window instead. The
  URL is handed to `explorer.exe` and launched **once**; the launcher's exit
  status is **not** inspected (explorer.exe exits 1 even on success), so there
  is no fallback launcher that could open the UI twice or start the browser
  elevated.
- The browser **never receives the Pi token**. It holds only an
  `HttpOnly; SameSite=Strict` session cookie for the local server, and the
  loopback server talks to the Pi on the browser's behalf. See
  [architecture.md](architecture.md).
- Set `"web_ui": { "enabled": false }` in `client.json` to disable it. Logging
  in requires the tray (and therefore an elevated process) to be running.

**Upgrade hazard.** The client config loader rejects unknown fields. If you roll
`yab.exe` back to a version older than the one that introduced `web_ui`, delete
the `"web_ui"` block from `%ProgramData%\you-as-bee\client.json` first, or the
older binary fails to start. The new default is enabled, so removing the key
does not disable the UI on the current version.

## 8. Tray, quitting and removal

- The tray menu shows each pin's state and, when relevant, the last attach
  error or the pause reason (for example "device was detached outside usbip").
- **Quit does not detach.** "Quit (devices stay attached)" only stops the tray
  and supervisor; the vhci ports remain and devices keep working. Use
  `yab detach --all` (elevated) to detach them.
- **Restart as administrator** relaunches the process through UAC and exits the
  original one, so there is only ever one tray.
- `yab uninstall` and `deploy\windows\uninstall.ps1` both stop the running tray
  first and both keep `%ProgramData%\you-as-bee\client.json` by default; pass
  `-RemoveConfig` to `uninstall.ps1` to delete it too.

Run the full [hardware validation checklist](hardware-validation.md).
