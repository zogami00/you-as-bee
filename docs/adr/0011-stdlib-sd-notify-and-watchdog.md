# 11. Standard-library sd_notify and a systemd watchdog

- Status: Accepted
- Date: 2026-10-05

## Context

`yabd` is a headless systemd service on a machine that may be physically hard to
reach. If it hangs, it should be restarted automatically. systemd's native
mechanism is `Type=notify` plus a watchdog: the service signals readiness and
periodically resets a timer, and systemd restarts it if the timer expires.
Using a third-party sd-notify library would add a dependency to the Linux agent.

## Decision

Implement the minimal `sd_notify` protocol in `internal/sdnotify` with the
standard library only: a `unixgram` write of `READY=1`, `WATCHDOG=1` and
`STATUS=...` to `$NOTIFY_SOCKET` (handling the abstract-socket `@` prefix), and
a no-op when `NOTIFY_SOCKET` is unset. The unit uses `Type=notify`,
`WatchdogSec=30`, `Restart=always`, and `StartLimitIntervalSec=0`. The agent
resets the watchdog every 10 seconds.

## Consequences

- A hung agent is killed and restarted by systemd; a headless Pi recovers
  itself. The hardware checklist verifies this with `kill -STOP`.
- No third-party dependency for a mechanism that is only a single datagram.
- The notify functions are no-ops outside systemd, so the binary also runs in
  the foreground for debugging.
- Shutdown deliberately does **not** unbind devices: a restart must not drop
  clients that are already attached.
