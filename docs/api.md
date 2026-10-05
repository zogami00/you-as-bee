# Management API

`yabd` serves a small HTTP/JSON API on its configured listen address
(default `0.0.0.0:3241`). It is a control plane only: it reports device state
and records intent. The reconcile loop performs the actual exports.

Base URL example: `http://raspberrypi.local:3241`.

## Authentication

Every route except `/healthz` is protected by two checks, in this order:

1. **CIDR allowlist.** The connection's peer address (`r.RemoteAddr`) must be
   inside one of `allowed_clients`. Forwarding headers such as
   `X-Forwarded-For` are deliberately ignored. Failure: `403 forbidden`.
   This applies to the browser UI routes (`/ui/...`) too, including the login
   form, logout and the assets: the allowlist is checked before any
   pre-authentication work happens.
2. **Credential.** Either
   - `Authorization: Bearer <64 hex chars>` equal to the token from
     `token_file`, compared in constant time; or
   - a valid **session cookie** issued by the browser login below.

Failure: `401 unauthorized`.

When the request is authenticated by a session cookie (not a bearer token), a
state-changing method (anything other than GET/HEAD/OPTIONS) must also carry
`X-YAB-CSRF: 1`. A missing header is `403 forbidden`, because the caller is
authenticated but the write is refused. Bearer clients never need the header.

`GET /healthz` is the only route registered without the guard. It returns `ok`
with no authentication and no allowlist check, so the client can distinguish
"unreachable" from "reachable but not allowed/authorised".

There is **no TLS**. See [security.md](security.md).

### Browser sessions (`/ui/`)

When `web_ui` is `true` (default `false`, see
[configuration.md](configuration.md)) the agent serves an embedded UI:

| Method | Path | Description |
|--------|------|-------------|
| GET | `/ui/login` | Token form. The Pi flow issues no one-time code; the token is the credential. |
| POST | `/ui/login` | Verify the token; sets the session cookie; `303` to `/ui/`. The body is capped at 4 KiB. |
| POST | `/ui/logout` | Delete the session and clear the cookie; `303` to `/ui/login`. Requires `X-YAB-CSRF: 1`. |
| GET | `/ui/` | The application shell. Redirects to `/ui/login` without a session. Any deeper `/ui/<path>` also serves the shell (a deliberate SPA fallthrough); the more specific `/ui/assets/` pattern still serves real files. |
| GET | `/ui/assets/{file}` | Embedded CSS and JavaScript. |

Every `/ui/` response carries `Content-Security-Policy` (no `unsafe-inline`),
`X-Content-Type-Options: nosniff` and `X-Frame-Options: DENY`, because the shell
exposes the destructive Export/Force/Reset controls.

The session cookie is `HttpOnly; SameSite=Strict`, holds an opaque 64-hex-char
id, and lives only in the agent's memory, so restarting `yabd` logs every
browser out. The idle TTL is 12 hours; the cookie's `Max-Age` is refreshed on
every session-authenticated request so it tracks that idle window rather than
expiring a fixed 12 hours after login. Sessions are capped at 32, least recently
used evicted, and login attempts are rate-limited to one per second **per peer
address**, so one client cannot lock the operator out. An open `/v1/events`
stream is closed as soon as its session ends. With `web_ui: false` every `/ui/`
path is `404` for an allowlisted peer and `403` for a non-allowlisted one.

## Routes

| Method | Path | Description |
|--------|------|-------------|
| GET | `/healthz` | Liveness. `text/plain`, body `ok`. No auth. |
| GET | `/v1/info` | Agent version, hostname, uptime and `usbipd` state. |
| GET | `/v1/devices` | All configured pins with their current state. |
| GET | `/v1/devices/{id}` | One pin. |
| POST | `/v1/devices/{id}/export` | Ask the agent to export the device (`202`, asynchronous). |
| POST | `/v1/devices/{id}/unexport` | Release the device from `usbip-host`. |
| POST | `/v1/devices/{id}/reset` | Clear backoff, failures and quarantine. |
| GET | `/v1/events` | Server-sent event stream. |
| GET | `/v1/logs` | Recent structured log records (`?after=<seq>&limit=<1..500>`). |

`{id}` is either the configured pin name or the bus id the agent currently
reports for a present device. An id that matches neither is `404 not_found`.
`export` accepts an optional query parameter `force=true`, which permits
disturbing a device a client is already attached to.

## Request and response bodies

There are no request bodies. `POST /v1/devices/{id}/unexport` and `reset`
return `200 OK` with:

```json
{ "status": "ok" }
```

`POST /v1/devices/{id}/export` returns `202 Accepted` with
`{ "status": "accepted" }`: the bind happens on the reconcile loop, so the
response acknowledges the request rather than confirming it. A caller that
needs to report success must poll `GET /v1/devices/{id}` until `state` is
`exported` or `in_use`. `yabd export` does exactly this.

### GET /v1/info

```json
{
  "version": "v0.1.0-3-gabc1234",
  "hostname": "raspberrypi",
  "uptime_sec": 4242,
  "usbipd_up": true
}
```

`usbipd_up` is a TCP probe of `127.0.0.1:3240`.

### GET /v1/devices

```json
{
  "devices": [
    {
      "pin": "xbox-wireless",
      "busid": "1-1.4",
      "vid": "045e",
      "pid": "02e6",
      "serial": "",
      "product": "Xbox Wireless Adapter",
      "driver": "usbip-host",
      "present": true,
      "state": "exported",
      "mode": "always"
    }
  ]
}
```

Device fields:

| Field | Meaning |
|-------|---------|
| `pin` | Stable pin name from the agent config. |
| `busid` | Current USB bus id, e.g. `1-1.4`. Empty when absent. |
| `vid`/`pid` | 4-hex-digit ids (lower case). Empty when absent. |
| `serial` | Device serial, omitted when empty. |
| `product` | Human-readable product string, omitted when empty. |
| `driver` | Kernel driver currently bound, omitted when unbound. |
| `present` | Physically attached to the Pi right now. |
| `state` | `unexported`, `exported`, `in_use`, `absent` or `error`. |
| `mode` | `always` or `on_demand`. |
| `last_error` | Short, path-free reason for the most recent bind/unbind failure, omitted when empty. Cleared by a successful bind, an explicit reset, or the device going absent. Filesystem detail is redacted; see [security.md](security.md). |

`GET /v1/devices/{id}` returns a single device object (no wrapper).

### GET /v1/events

`text/event-stream`. Each event is:

```
event: state_changed
data: {"type":"state_changed","device":{...},"at":"2026-01-02T15:04:05Z"}
```

Event `type` is one of `device_added`, `device_removed` or `state_changed`.
A keepalive comment (`: keepalive`) is sent every 20 seconds so idle
connections and NAT mappings stay alive. The server clears the write deadline
for this route, so the stream is not killed by the normal request timeout.
`state_changed` is also raised when only a device's `last_error` changes.

### GET /v1/logs

Returns the most recent records from the agent's in-memory log ring (1000
entries, info level and above):

```json
{
  "entries": [
    {
      "seq": 1,
      "time": "2026-01-02T15:04:05Z",
      "level": "info",
      "msg": "yabd started",
      "attrs": { "listen": "0.0.0.0:3241", "pins": "2" }
    }
  ],
  "next": 1
}
```

`after` is the last sequence number the caller has seen (default `0`);
`limit` is clamped to `1..500` (default `100`). `next` is the sequence number to
pass as the following `after` value. `attrs` is omitted when a record has no
attributes. Filesystem paths in a record's message and attributes are redacted
before they are returned, matching the `last_error` redaction; the full record
is still written to the agent's own log/journal.

## Error body

Errors use one shape:

```json
{ "code": "unauthorized", "message": "missing or invalid credentials" }
```

| Status | `code` | When |
|--------|--------|------|
| 400 | `bad_request` | Malformed `/v1/logs` `limit`, or a login form that is malformed or over the 4 KiB body cap. |
| 401 | `unauthorized` | Missing or wrong bearer token / session. |
| 403 | `forbidden` | Peer address not in `allowed_clients` (on any route, including `/ui/...`), a session write without `X-YAB-CSRF`, or a session write whose `Origin` host does not match. |
| 404 | `not_found` | Unknown device id, or any `/ui/` path with `web_ui: false` (when the peer is allowlisted). |
| 429 | `too_many_requests` | Login attempts from the same peer faster than one per second (rendered form). |
| 500 | `internal` | Backend failure. The message is generic; the detail is logged, never returned. |
| 500 | `sse_unsupported` | Response writer cannot stream (should not happen with net/http). |

The typed Go client maps any non-2xx to an `APIError`; if the body is not the
standard error object the code falls back to `http_error`.

## Server limits

`ReadHeaderTimeout` 10s, `ReadTimeout` 30s, `IdleTimeout` 120s,
`WriteTimeout` 30s (overridden for SSE responses).

## Raw example

```bash
TOKEN=$(sudo cat /etc/you-as-bee/token)

curl -sS -H "Authorization: Bearer $TOKEN" http://raspberrypi.local:3241/v1/devices

curl -sS -X POST -H "Authorization: Bearer $TOKEN" \
  "http://raspberrypi.local:3241/v1/devices/xbox-wireless/export?force=true"

curl -sS -N -H "Authorization: Bearer $TOKEN" \
  -H "Accept: text/event-stream" http://raspberrypi.local:3241/v1/events

curl -sS -H "Authorization: Bearer $TOKEN" \
  "http://raspberrypi.local:3241/v1/logs?after=0&limit=100"
```

The browser UI (when `web_ui` is enabled) is at
`http://raspberrypi.local:3241/ui/`; it redirects to `/ui/login` and then makes
the same `/v1/` calls above using its session cookie.


