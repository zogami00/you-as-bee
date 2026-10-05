# Management API

`yabd` serves a small HTTP/JSON API on its configured listen address
(default `0.0.0.0:3241`). It is a control plane only: it reports device state
and records intent. The reconcile loop performs the actual exports.

Base URL example: `http://raspberrypi.local:3241`.

## Authentication

Every route under `/v1/` is protected by two checks, in this order:

1. **CIDR allowlist.** The connection's peer address (`r.RemoteAddr`) must be
   inside one of `allowed_clients`. Forwarding headers such as
   `X-Forwarded-For` are deliberately ignored. Failure: `403 forbidden`.
2. **Bearer token.** `Authorization: Bearer <64 hex chars>` must equal the
   token from `token_file`, compared in constant time. Failure:
   `401 unauthorized`.

`GET /healthz` is the only route registered without the guard. It returns `ok`
with no authentication and no allowlist check, so the client can distinguish
"unreachable" from "reachable but not allowed/authorised".

There is **no TLS**. See [security.md](security.md).

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

## Error body

Errors use one shape:

```json
{ "code": "unauthorized", "message": "missing or invalid bearer token" }
```

| Status | `code` | When |
|--------|--------|------|
| 401 | `unauthorized` | Missing or wrong bearer token. |
| 403 | `forbidden` | Peer address not in `allowed_clients`. |
| 404 | `not_found` | Unknown device id. |
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
```


