# Security

This is an honest description of what you-as-bee protects and what it does not.
It is a home-LAN tool, not a hardened remote-access product.

## The trust model in one line

Two people on the same LAN segment can already do almost anything to each
other; you-as-bee does not try to survive an attacker who is on that segment.
It does try to keep the management API from being an open door.

## What is enforced

### Bearer token

Every `/v1/` route requires `Authorization: Bearer <64 hex chars>`. The token
is generated on the Pi from `/dev/urandom`, stored at
`/etc/you-as-bee/token` mode `0600`, and compared in constant time
(`crypto/subtle.ConstantTimeCompare`). It is printed once by `provision.sh` for
the operator to copy to the Windows client.

### CIDR allowlist

The peer address (`r.RemoteAddr`) must be inside one of `allowed_clients`.
Forwarding headers are ignored, so a client cannot claim a source address it
does not have.

### Firewall

`provision.sh` installs an nftables rule that accepts TCP 3240 and 3241 only
from `allowed_clients` and drops them from everything else. See
[setup-pi.md](setup-pi.md).

## What is **not** enforced - read this part

### There is no TLS

The token is sent in cleartext on every request. So is all API traffic and
every server-sent event. Anyone who can observe the LAN traffic can read the
token and then use the API. There is no confidentiality and no integrity: an
on-path attacker can modify responses. TLS is deliberately out of scope for
this MVP, but "deliberate" is not "safe".

### usbipd on 3240 is unauthenticated

The USB/IP data plane uses the stock Linux `usbipd`. It has **no
authentication and no encryption**. Any host that can open a TCP connection to
3240 can list the exported devices and attach to one. Attaching means the
remote host gets raw USB access to the device: it can read every input report,
and for a Bluetooth dongle it can use the radio.

The nftables rule restricts 3240 to `allowed_clients`. That is a
**network-layer** control, not an authentication control. An attacker who can
source an address inside the allowlist (for example, another device on the same
subnet) can reach 3240. The allowlist is a fence, not a lock.

### The token protects the API, not the devices

The token gates export/unexport/reset. It has no effect on who may attach to an
already-exported device over USB/IP. Once a device is exported (especially in
`always` mode), anyone who can reach 3240 can use it.

## What this protects against

- A host outside the configured subnets reaching the management API or the
  USB/IP port (firewall plus allowlist).
- Accidental or unauthorised API calls from a machine without the token.
- Another client on the LAN guessing the API and toggling exports.

## What it does not protect against

- Passive or active attackers on the same segment (token and traffic are
  cleartext).
- Anyone who can source an allowlisted address (USB/IP has no auth).
- Physical access to the Pi, or to the SD card (the token is readable as root).
- A malicious USB device on the Pi's hub.

## Windows driver signing

The Windows side needs the `usbip-win2` vhci driver. If it is unsigned and you
enable test-signing mode, Windows disables driver signature enforcement
machine-wide and **kernel anti-cheat refuses to run** in that mode. That is a
correctness and compatibility trade-off, not a security boundary; it makes the
machine attackable by any kernel driver. Prefer a signed `usbip-win2` release
and leave test-signing off. See [setup-windows.md](setup-windows.md).

## Practical guidance

- Keep `allowed_clients` as tight as possible (one subnet, not all RFC1918).
- Use a wired or WPA2/WPA3 network you control; do not expose 3240/3241 to the
  internet.
- Treat the token like a password: it is only as private as the LAN.
- Prefer `on_demand` over `always` for devices you do not need exported
  continuously, so the attach window is smaller.
- If you need integrity or confidentiality, put the Pi and PC on a dedicated
  VLAN or tunnel (WireGuard, etc.) and treat that as the security boundary.
