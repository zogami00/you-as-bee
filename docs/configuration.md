# Configuration

Both binaries read strict JSON. The rules are the same for both:

- Unknown fields are **rejected** (the loader uses `DisallowUnknownFields`).
- Any trailing data after the first JSON document is rejected.
- **JSON has no comments** - do not add `//` or `/* */`; the file is a single
  JSON object.
- Durations are Go duration strings (`"5s"`, `"1m30s"`), never bare numbers.
- Every string value is passed through `${NAME}` expansion from the environment
  before decoding, so `${YAB_TOKEN}` becomes the value of `YAB_TOKEN` (an unset
  variable expands to empty).
- `schema_version` must be `1`.

The agent config lives at `/etc/you-as-bee/agent.json`; the client config at
`%ProgramData%\you-as-bee\client.json`. Deploy-side templates are
`deploy/pi/agent.example.json` and `deploy/windows/client.example.json`.

## Agent (`yabd`)

| Field | Type | Default | Meaning |
|-------|------|---------|---------|
| `schema_version` | int | `1` | Must be `1`. |
| `listen` | string | `0.0.0.0:3241` | Management API bind address. USB/IP itself is always 3240 and is not configurable. |
| `token_file` | string | `/etc/you-as-bee/token` | File holding the bearer token (trimmed of surrounding whitespace). |
| `allowed_clients` | string[] | RFC1918 ranges | CIDR allowlist for the API, checked against the connection's peer address. |
| `poll_interval` | duration | `5s` | Reconcile period. Must be positive. |
| `usbip.bin` | string | `/usr/sbin/usbip` | Path to the `usbip` client tool. |
| `usbip.usbipd_bin` | string | `/usr/sbin/usbipd` | Path to the `usbipd` daemon. |
| `usbip.manage_usbipd` | bool | `true` | Parsed and validated but not read by the runtime today; `usbipd` is always run as its own systemd unit. |
| `devices[].name` | string | - | Stable pin id, `^[a-z0-9-]{1,32}$`, unique. |
| `devices[].vid` | string | - | 4-hex-digit vendor id; case-normalised to lower case. |
| `devices[].pid` | string | - | 4-hex-digit product id; case-normalised to lower case. |
| `devices[].serial` | string | unset | Optional exact serial match (narrowing selector). |
| `devices[].port` | string | unset | Optional exact bus-id match, e.g. `1-1.4` (narrowing selector). |
| `devices[].mode` | string | `on_demand` | `always` exports as soon as the device is present; `on_demand` waits for a request. |
| `log_level` | string | `info` | `debug`, `info`, `warn` or `error`. |
| `log_format` | string | `text` | `text` or `json`. |

`vid`/`pid` alone match any device with that pair. When more than one attached
device matches a pin and no `serial`/`port` narrows it, the device is
**ambiguous**: it is reported and never guessed.

## Client (`yab`)

| Field | Type | Default | Meaning |
|-------|------|---------|---------|
| `schema_version` | int | `1` | Must be `1`. |
| `servers[].name` | string | - | Unique, human-facing server id. |
| `servers[].host` | string | - | DNS name or IP of the agent. Unique. |
| `servers[].api_port` | int | `3241` | Management API port, 1-65535. |
| `servers[].token` | string | - | Bearer token, exactly 64 lower-case hex characters. |
| `usbip_path` | string | unset | Optional explicit path to `usbip.exe`. When empty, the client tries the registry, `%ProgramFiles%\USBip`, then `PATH`. |
| `auto_attach[].server` | string | - | Must name a configured server. |
| `auto_attach[].device` | string | - | A pin name on that server. Pairs must be unique. |
| `reconnect.initial` | duration | `1s` | First backoff delay. |
| `reconnect.max` | duration | `30s` | Backoff ceiling; must not be less than `initial`. |
| `command_timeout` | duration | `15s` | Per-request API timeout. Must be positive. |
| `log_file` | string | unset | When set, the client appends its log (attach failures, external-detach notifications) to this file; when empty, logging is discarded. The tray task runs as `yab.exe tray`, so use an absolute path the account can write. |
| `log_level` | string | `info` | `debug`, `info`, `warn` or `error`. |

## Full agent example

This is `deploy/pi/agent.example.json` (and what `provision.sh` derives
`/etc/you-as-bee/agent.json` from). It validates as written.

```json
{
  "schema_version": 1,
  "listen": "0.0.0.0:3241",
  "token_file": "/etc/you-as-bee/token",
  "allowed_clients": [
    "192.168.0.0/16",
    "10.0.0.0/8",
    "172.16.0.0/12"
  ],
  "poll_interval": "5s",
  "usbip": {
    "bin": "/usr/sbin/usbip",
    "usbipd_bin": "/usr/sbin/usbipd",
    "manage_usbipd": true
  },
  "devices": [
    {
      "name": "bluetooth",
      "vid": "0a12",
      "pid": "0001",
      "mode": "on_demand"
    },
    {
      "name": "xbox-wireless",
      "vid": "045e",
      "pid": "02e6",
      "mode": "always"
    }
  ],
  "log_level": "info",
  "log_format": "text"
}
```

Notes:

- The VID/PID pairs are the ones the shipped udev rule and modprobe blacklist
  use (`0a12:0001` is a common CSR Bluetooth dongle; `045e:02e6` is an Xbox
  Wireless Adapter for Windows). Change them to match your dongles.
- If your device reports a serial number, add `"serial": "..."` to the pin to
  keep it tied to one physical device even if a second identical dongle appears.
- `provision.sh` rewrites `usbip.bin`/`usbip.usbipd_bin` to the actual installed
  paths it detects, because the packaged path can differ from the defaults.

## Full client example

This is `deploy/windows/client.example.json`. The token is a placeholder: put
the token `provision.sh` printed on the Pi here. It validates as written
(64 lower-case hex characters).

```json
{
  "schema_version": 1,
  "servers": [
    {
      "name": "pi",
      "host": "raspberrypi.local",
      "api_port": 3241,
      "token": "0000000000000000000000000000000000000000000000000000000000000000"
    }
  ],
  "usbip_path": "",
  "auto_attach": [
    { "server": "pi", "device": "xbox-wireless" },
    { "server": "pi", "device": "bluetooth" }
  ],
  "reconnect": { "initial": "1s", "max": "30s" },
  "command_timeout": "15s",
  "log_file": "",
  "log_level": "info"
}
```

`auto_attach[].device` is the **pin name** from the agent config, not the USB
bus id. The bus id changes across re-plugs; the pin is stable.
