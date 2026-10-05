# 12. Windows client elevation via a highest-privilege logon task

- Status: Accepted
- Date: 2026-10-05

## Context

Attaching a device to a `usbip-win2` vhci port requires administrator rights.
The client should re-attach automatically after logon and after a network blip,
without a UAC prompt every time. Running the whole client as an always-on
system service would detach it from the interactive desktop, which is where the
tray belongs.

## Decision

Register a **logon Scheduled Task with highest privileges** that runs
`yab.exe tray` for the current user (`schtasks /SC ONLOGON /RL HIGHEST`). The
task is created by `yab install` and by `deploy/windows/install.ps1`. When the
tray is started without elevation, it offers a "Restart as administrator" item
that relaunches via the UAC `runas` verb.

## Consequences

- Auto-attach works from logon without repeated UAC prompts, because the task
  itself runs elevated.
- The tray runs in the user's session, so the notification area works.
- Elevation is explicit and inspectable (`schtasks /Query /TN
  you-as-bee-client`), and removal is symmetric (`yab uninstall`). The elevation
  check uses the token's elevation type, so group membership alone is not enough.
- The task runs only at logon, so a manually-detached device stays detached
  until the next logon or `yab attach`.
