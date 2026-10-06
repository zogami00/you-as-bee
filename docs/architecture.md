# Architecture

you-as-bee is two programs and one wire protocol spoken over the LAN.

- `yabd` runs on the Raspberry Pi. It pins USB devices, exports them with the
  in-kernel `usbip-host` driver, and serves a small management API.
- `yab` runs on the Windows PC. It drives `usbip-win2` so the exported devices
  appear as real local USB devices, and it keeps them attached.
- USB/IP itself (TCP 3240) is the existing Linux `usbip`/`usbipd` tooling.
  you-as-bee orchestrates it; it does not reimplement it.

```
   Windows PC (yab.exe)                        Raspberry Pi (yabd)
 +------------------------------+          +-------------------------------+
 | yab tray                     |          | yabd  (systemd, Type=notify)  |
 |   supervisor: auto-attach    |  :3241   |   reconcile loop (owns sysfs) |
 |   usbip-win2 usbip.exe       |<-------->|   management HTTP API + SSE   |
 |                              | HTTP/SSE |   sd_notify: READY/WATCHDOG   |
 +--------------+---------------+          +---------------+---------------+ 
                |                                          |
                |  USB/IP over TCP 3240                    | USB
                +----------------------------------------->|  powered hub
                                                           |   + BT dongle
                                                           |   + Xbox dongle
                                                           +---------------+
```

## The two processes

### yabd (Pi)

`yabd run` (the systemd `ExecStart`) does four things:

1. Loads and validates `/etc/you-as-bee/agent.json` and reads the bearer token
   from `token_file` (default `/etc/you-as-bee/token`).
2. Starts one reconcile loop (`internal/agent`) and one HTTP server
   (`internal/api`) on the configured `listen` address (default `0.0.0.0:3241`).
3. Sends `READY=1` to systemd and resets the watchdog every 10 seconds.
4. On shutdown it deliberately **leaves devices exported**: a stopped agent must
   not drop clients that are already attached.

The reconcile loop is the only goroutine that writes to sysfs. Every API method
only records intent and wakes the loop; it never touches a device directly.

### yab (Windows)

`yab tray` runs the supervisor (`internal/client`) and the notification-area UI.
The supervisor:

- probes each server's `/healthz` and reads `/v1/devices`;
- asks the agent to export an `on_demand` device the first time it needs it;
- calls `usbip.exe attach` and confirms the local vhci port appeared;
- consumes the agent's SSE stream (`/v1/events`) so a re-plug is acted on
  immediately rather than at the next poll;
- backs off exponentially against a down API and against failing attaches;
- distinguishes a transient drop (recent API/SSE outage: re-attach) from a
  detach performed outside usbip (pause auto-attach and notify).

Pins are addressed by `(server, device)`, not by device name alone: the config
allows the same device name on two servers, and Pi bus ids such as `1-1.4`
repeat across Pis. Both events and attach/detach act on the server they belong
to, and the supervisor only detaches a stale vhci port when its host resolves
to the same server (`usbip-win2` may print the resolved IP rather than the
configured name).

Pausing is in memory only, so a restart or logon restores auto-attach. A
`device_added` event clears a pause - including an external-detach pause - on
the assumption that a physical re-plug is the user's intent to use the device
again. `yab detach` (or the tray's Detach item) is required to keep a device
detached; quitting the tray does not detach ports (see
[troubleshooting.md](troubleshooting.md)).

Detaching also runs `usbip attach --stop-all`, which is **global to the
machine**: it cancels every active attach attempt the usbip-win2 driver is
running, not only the one for the port being detached, so it can abort an
attempt for a different server or device. It exists because an automatic retry
started by an older `yab` (before attaches passed `--once`) lives inside the
driver, not in a `usbip` process, and detaching the port does not stop it.
usbip-win2 exposes no per-device cancel, and `Tool.Detach` only has the local
port number, so the call cannot be narrowed without a wider API change. Since
current `yab` always attaches with `--once` it starts no retries of its own, so
in normal operation the call is a no-op; the side effect only bites when another
usbip-win2 user or an older `yab` left a retry running.

## Windows local web UI

`yab tray` can serve the same embedded shell (`internal/webui/assets`) from a
loopback-only HTTP server (`internal/webui/local.go`). It is a small controller,
not a second supervisor:

- It binds `127.0.0.1` only; the Host header must equal the exact address it is
  serving on, which blocks DNS rebinding. It never answers a CORS preflight and
  refuses cross-origin requests.
- The browser authenticates with a **one-time code** the tray mints and opens in
  the default browser through `explorer.exe` (so the browser starts
  de-elevated). The code is single-use, expires after 60 seconds and is bound to
  the browser that loaded it; redeeming it (a `303`) sets an
  `HttpOnly; SameSite=Strict` session cookie and leaves the code out of the URL.
  Session writes require `X-YAB-CSRF: 1`.
- The code travels as a **path segment** (`GET /ui/login/<code>`), never a query
  string. `explorer.exe` rejects a URL containing `?` and opens a folder instead
  of the browser, which would break the de-elevated launch. `NewLoginURL` has a
  test asserting the URL stays query-free; when `explorer.exe` nevertheless
  fails, the launcher falls back to `rundll32.exe url.dll,FileProtocolHandler`.
- **The browser never sees a Pi token.** The local server's payload types carry
  no credential; `Attach`/`Detach` are forwarded in-process to the same
  supervisor methods the tray menu calls, so the loopback server talks to the Pi
  with the token from `client.json` and the browser only ever holds a local
  session cookie. There is a test asserting the adapter cannot marshal the
  token.
- State is `Status()` (pins) plus a server/reachability view cached for up to
  30 seconds, so a page load cannot trigger repeated probing. The SSE endpoint
  compares `Status()` against the previous snapshot once a second and emits only
  on change, rather than hooking the manager, so no transition is missed.

Disabling `web_ui.enabled` does not weaken the Pi-side API: the agent's own
`/v1` surface and its optional browser UI are unchanged.

## Management API role

The API is control plane only. It reports device state and records intent
(export, unexport, reset). It does not export anything itself. This keeps the
bind sequence single-threaded and race-free: an `export` request just sets a
flag and pokes the reconciler, which performs the sysfs writes on its own
goroutine.

## State machine

The reconciler tracks one lifecycle state per configured pin
(`internal/agent/state.go`):

```
 Absent  --device appears-->  Present
    ^                            |
    |                     desired?|  (always mode, or an explicit export)
    |                            v
    |              +--------> Binding --ok--> Exported --client attaches--> Attached
    |              |             |                 |                            |
    |              |           fail                |<------ client detaches -----+ 
    |              |             v
    |              +---------- Backoff  -- >5 failures in 5 min -- > Quarantined
    |                                                              (10 min)
    +--------------------------------------------------------------+
                         device disappears / not desired
```

Reported states (the API's `state` values) are a projection of the above:

| Internal state        | API `state` |
|-----------------------|-------------|
| Absent                | `absent`    |
| Present, Binding      | `unexported`|
| Exported              | `exported`  |
| Attached              | `in_use`    |
| Backoff, Quarantined  | `error`     |

The `always` and `on_demand` modes only affect whether the device is exported
without an explicit request. An `on_demand` device still stays exported once a
client has asked for it, until it is unexported or disappears.

## Bind sequence

`internal/usbiphost` performs a fixed, ordered sequence, each step rolled back if
a later step fails:

1. **Preflight** - require root; if
   `/sys/bus/usb/drivers/usbip-host` is missing, `modprobe -a usbip-core
   usbip-host`.
2. **Re-read identity** - re-read VID/PID/serial from sysfs and confirm the
   device did not change underneath us.
3. **Refuse an attached client** - if `usbip_status` is `2` (attached) and
   `force` is not set, return `ErrInUse`.
4. **Disable autosuspend** - write `on` to `power/control`.
5. **Claim the busid** - write `add <busid>` to `usbip-host/match_busid`.
6. **Rebind** - unbind the current driver when one is bound (`btusb`, `xone`,
   ...), then bind `usbip-host`.
7. **Verify** - the driver symlink must be `usbip-host`, `usbip_status` must be
   `1`, and the device generation (`devnum`) must not have changed.

If any step fails after the power write, rollback unbinds, removes the
`match_busid` entry and re-probes the original driver.

`Unbind` is idempotent per step. It only skips work when the device is gone;
otherwise it unbinds `usbip-host` when it is the current driver, always drops
the `match_busid` entry (tolerating `ENODEV` for a busid that was never added,
and `EINVAL` defensively), always re-probes, and restores `power/control`. This
matters when an earlier `Unbind` failed between writes: the device is off
`usbip-host` and has no driver, so a retry must complete the remaining steps
instead of treating it as a no-op.

The generic USB device driver `usb` is not treated as a wrong driver. On a real
Pi the device-level `driver` symlink is `usb` for every un-exported device (the
function driver such as `btusb` binds to the interfaces), so recovery is only
triggered by `usbip_status == 3` or by a non-generic driver that is not
`usbip-host`. A device with no device-level driver at all has no `driver/unbind`
to write, so the bind sequence skips that step and proceeds directly to
`match_busid` + `usbip-host/bind`.

Every sysfs write is performed by the reconcile goroutine. `ReconcileOnce`
serializes whole passes (so two concurrent callers cannot double-bind) and holds
the state mutex only to plan a pass and to commit its outcome; the Binder calls
(and any `modprobe`) run with the lock released, so API status calls stay
responsive while a bind is in flight.

A forced export is consumed only by the pass that actually used it (a pass that
acts on the pin with `force` set), so a later attach is refused again unless
forced again. A force issued against a device that is already healthy and
exported has no action to run and is cleared immediately, so it cannot survive
to disturb a client that attaches later.

## Backoff and quarantine

Per pin, on bind/reset failure:

- backoff starts at 1s and doubles up to 60s, with +/-20% jitter;
- more than 5 failures inside a rolling 5-minute window trip quarantine for
  10 minutes;
- a device that stays exported for 2 minutes clears its failure history.

Quarantine is cleared by the quarantine timer or by an explicit reset
(`yabd reset <pin>`). Only the reconcile loop acts, so the backoff clock is
deterministic and testable.

## The wire protocol

Two distinct protocols share the LAN:

- **Control plane** - the management API on TCP 3241, HTTP with a bearer token
  and a CIDR allowlist. See [api.md](api.md).
- **Data plane** - USB/IP on TCP 3240, served by `usbipd` with no
  authentication. See [security.md](security.md).

Port 3240 is fixed and deliberately not configurable.
