# 13. Pin the usbip-win2 version; never toggle test-signing or Secure Boot automatically

- Status: Accepted
- Date: 2026-10-05

## Context

The Windows client depends on `usbip-win2` and its `vhci` driver, which are
developed separately and change between releases. A floating version can break
the argument vectors, output parsing or driver behaviour the client relies on.
Driver signing is the sharp edge: an untrusted driver is blocked with Code 52
unless test-signing is enabled, and enabling test-signing disables driver
signature enforcement machine-wide, requires Secure Boot off, and makes kernel
anti-cheat refuse to run. Silently changing boot configuration would be
surprising and, for a gaming machine, harmful.

## Decision

- **Pin** the `usbip-win2` release the operator installs, and let the installer
  verify a SHA-256 of a release archive when one is supplied
  (`install.ps1 -UsbipArchive ... -UsbipSha256 ...`). Document the pinned
  version in `docs/setup-windows.md`.
- **Never** change test-signing or Secure Boot automatically. `install.ps1` and
  `yab doctor` only *report* the state they find (`bcdedit /enum {current}` and
  the Secure Boot registry value). Enabling test-signing remains a documented,
  manual operator decision.

## Consequences

- A known-good `usbip-win2` release is reproducible; upgrades are explicit.
- The installer never reboots or reconfigures the boot loader, so it cannot
  silently break anti-cheat or machine security.
- The operator must make the driver-signing trade-off knowingly; it is described
  in `docs/setup-windows.md`, including the anti-cheat consequence.
- If a future release changes its interface, the client's parsing and
  invocation need updating, which is why the version is pinned rather than
  tracked.
