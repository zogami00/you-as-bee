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
| `allowed_clients` | string[] | loopback + RFC1918 ranges | CIDR allowlist for the API, checked against the connection's peer address. |
| `poll_interval` | duration | `5s` | Reconcile period. Must be positive. |
| `usbip.bin` | string | `/usr/sbin/usbip` | Path to the `usbip` client tool. |
| `usbip.usbipd_bin` | string | `/usr/sbin/usbipd` | Path to the `usbipd` daemon. `usbipd` runs as its own systemd unit; the agent never starts or stops it. |
| `devices[].name` | string | - | Stable pin id, `^[a-z0-9-]{1,32}$`, unique. |
| `devices[].vid` | string | - | 4-hex-digit vendor id; case-normalised to lower case. |
| `devices[].pid` | string | - | 4-hex-digit product id; case-normalised to lower case. |
| `devices[].serial` | string | unset | Optional exact serial match (narrowing selector). |
| `devices[].port` | string | unset | Optional exact bus-id match, e.g. `1-1.4` (narrowing selector). |
| `devices[].mode` | string | `on_demand` | `always` exports as soon as the device is present; `on_demand` waits for a request. |
| `log_level` | string | `info` | `debug`, `info`, `warn` or `error`. |
| `log_format` | string | `text` | `text` or `json`. |
| `web_ui` | bool | `false` | Serve the embedded browser UI at `/ui/` with session-cookie auth. Opt-in: upgrading never enables it. |

`vid`/`pid` alone match any device with that pair. When more than one attached
device matches a pin and no `serial`/`port` narrows it, the device is
**ambiguous**: it is reported and never guessed.

`127.0.0.0/8` is included in the default `allowed_clients` so the on-Pi CLI
(`yabd status`, `export`, `unexport`, `reset`) can reach the agent at
`http://127.0.0.1:3241`. `provision.sh` always prepends `127.0.0.0/8` to
whatever `--client-cidr` list you give it, and the nftables rule accepts
loopback independently of the allowlist, so the CLI keeps working even with a
restrictive CIDR. If you edit `agent.json` by hand instead of re-running
`provision.sh`, keep a loopback range or those commands get `403 forbidden`.

## Client (`yab`)

| Field | Type | Default | Meaning |
|-------|------|---------|---------|
| `schema_version` | int | `1` | Must be `1`. |
| `servers[].name` | string | - | Unique, human-facing server id, `^[a-z0-9-]{1,32}$` (it prefixes every qualified pin id, so `/` is not allowed). |
| `servers[].host` | string | - | DNS name or IP of the agent. Unique. |
| `servers[].api_port` | int | `3241` | Management API port, 1-65535. |
| `servers[].token` | string | - | Bearer token, exactly 64 lower-case hex characters. |
| `usbip_path` | string | unset | Optional explicit path to `usbip.exe`. When empty, the client tries the registry, `%ProgramFiles%\USBip`, then `PATH`. |
| `auto_attach[].server` | string | - | Must name a configured server. |
| `auto_attach[].device` | string | - | A pin name on that server. Pairs must be unique. |
| `reconnect.initial` | duration | `1s` | First backoff delay. |
| `reconnect.max` | duration | `30s` | Backoff ceiling; must not be less than `initial`. |
| `command_timeout` | duration | `15s` | Per-request API timeout. Must be positive. |
| `receive_mode` | string | `low-latency` | usbip-win2 attach receive mode: `low-latency` or `zero-copy`. `zero-copy` is usbip-win2's own default and can flood Windows with device-change events and USB stalls against some devices; `low-latency` is the default here. Requires **usbip-win2 >= 0.9.8.0**: the `--receive-mode` flag does not exist in v0.9.7.7 or earlier, where attach fails with `usbipwin: usbip exited N`. |
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
    "127.0.0.0/8",
    "192.168.0.0/16",
    "10.0.0.0/8",
    "172.16.0.0/12"
  ],
  "poll_interval": "5s",
  "usbip": {
    "bin": "/usr/sbin/usbip",
    "usbipd_bin": "/usr/sbin/usbipd"
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
  "log_format": "text",
  "web_ui": false
}
```

Notes:

- `web_ui` is off by default. Enable it only on a network you trust: the UI
  runs over the same cleartext HTTP as the API (see
  [security.md](security.md)). The log endpoint `GET /v1/logs` is available to
  any authenticated caller whether or not `web_ui` is enabled. The `/ui/` routes
  are behind the same `allowed_clients` allowlist as `/v1/`.
- **Upgrade ordering.** The loader rejects unknown fields, so a `yabd` older
  than the one that introduced `web_ui` will reject a config containing the key
  and crash-loop. The shipped example and therefore every config
  `provision.sh` creates carries `"web_ui"`, and the script only adds keys. If
  you roll the binary back past this version, delete the `"web_ui"` line from
  `/etc/you-as-bee/agent.json` (strict JSON: it cannot be commented out) before
  starting the older binary. See [setup-pi.md](setup-pi.md).
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
  "receive_mode": "low-latency",
  "log_file": "",
  "log_level": "info"
}
```

`auto_attach[].device` is the **pin name** from the agent config, not the USB
bus id. The bus id changes across re-plugs; the pin is stable.
