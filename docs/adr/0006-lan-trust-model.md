# 6. LAN trust model: bearer token plus CIDR allowlist, no TLS

- Status: Accepted
- Date: 2026-10-05

## Context

The management API controls which USB devices are exported and to whom. It must
not be an open door, but the product is a home-LAN tool and the data plane
(`usbipd` on 3240) is stock USB/IP with no authentication at all. Adding TLS
would add certificates, rotation and trust distribution to a tool that is run on
a trusted private network, and would not fix the unauthenticated data plane.

## Decision

Protect the management API with a **bearer token** (64 hex chars, compared in
constant time) plus a **CIDR allowlist** checked against the transport peer
address. Deliver over plain HTTP: **no TLS**. Restrict 3240 and 3241 to the
allowlist with an nftables rule on the Pi. Document the limits honestly rather
than implying the API is secure against a LAN attacker.

## Consequences

- The token and all traffic are cleartext; an on-path attacker can read the
  token and use the API. There is no integrity or confidentiality.
- `usbipd` on 3240 remains unauthenticated; the allowlist is a network fence,
  not authentication. Anyone who can source an allowlisted address can attach to
  an exported device.
- Deployment is simple: no certificates to issue or renew.
- The honest trade-offs are recorded in `docs/security.md`, including the
  recommendation to use a VLAN or tunnel if stronger protection is needed.
- Superseding this ADR (adding TLS, or an authenticated data plane) would be a
  significant security improvement and should be recorded as a new decision.
