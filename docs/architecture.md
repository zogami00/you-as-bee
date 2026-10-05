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
6. **Rebind** - unbind the current driver (`btusb`, `xone`, ...) then bind
   `usbip-host`.
7. **Verify** - the driver symlink must be `usbip-host`, `usbip_status` must be
   `1`, and the device generation (`devnum`) must not have changed.

If any step fails after the power write, rollback unbinds, removes the
`match_busid` entry and re-probes the original driver.

`Unbind` is idempotent per step. It only skips work when the device is gone;
otherwise it unbinds `usbip-host` when it is the current driver, always drops
the `match_busid` entry (tolerating `EINVAL` for an unknown busid), always
re-probes, and restores `power/control`. This matters when an earlier `Unbind`
failed between writes: the device is off `usbip-host` and has no driver, so a
retry must complete the remaining steps instead of treating it as a no-op.

Every sysfs write is performed by the reconcile goroutine. `ReconcileOnce`
holds the state mutex only to plan a pass and to commit its outcome; the Binder
calls (and any `modprobe`) run with the lock released, so API status calls stay
responsive while a bind is in flight.

A forced export is one-shot: the `force` flag is consumed by the pass that acts
on it and then cleared, so a later attach is refused again unless forced.

## Backoff and quarantine

Per pin, on bind/reset failure:

- backoff starts at 1s and doubles up to 60s, with +/-20% jitter;
- more than 5 failures inside a rolling 5-minute window trip quarantine for
  10 minutes;
- a device that stays exported for 2 minutes clears its failure history.

Quarantine is cleared by the timer, by an explicit `reset`, or by a
`yabd reset <pin>`. Only the reconcile loop acts, so the backoff clock is
deterministic and testable.

## The wire protocol

Two distinct protocols share the LAN:

- **Control plane** - the management API on TCP 3241, HTTP with a bearer token
  and a CIDR allowlist. See [api.md](api.md).
- **Data plane** - USB/IP on TCP 3240, served by `usbipd` with no
  authentication. See [security.md](security.md).

Port 3240 is fixed and deliberately not configurable.
