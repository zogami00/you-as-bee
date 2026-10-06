# 14. Embedded stdlib web UI with session cookies

- Status: Accepted
- Date: 2026-10-05

## Context

The management API already works, but driving it means `curl` or the CLI. A
browser UI makes the device list, state and logs legible, and the same shell can
eventually be served by both `yabd` (the Pi agent) and `yab` (the Windows
client). Two constraints shape how it can be built:

- The project ships two static binaries with `CGO_ENABLED=0` and **no build
  step**. There is no Node, no bundler and no runtime dependency to install.
- The API has no TLS and the browser must not become a place where a long-lived
  bearer token leaks. A token in JavaScript-readable storage is readable by any
  script and survives until the browser storage is cleared.

The question is not "can we add a fancy SPA" but "what is the smallest UI that
does not enlarge the credential surface".

## Decision

- **Ship a small vanilla HTML/CSS/JS shell embedded in the Go binary** with
  `//go:embed`. There is no npm, no transpiler and no third-party JavaScript.
  Any asset the browser loads is compiled into `yabd`/`yab` and served by the
  same process that owns the API.
- **The browser authenticates with an `HttpOnly; SameSite=Strict` session
  cookie only.** The token is submitted once, over the same cleartext channel as
  every other API call, to `POST /ui/login`; the server compares it in constant
  time and returns an opaque in-memory session id. JavaScript cannot read the
  cookie, and `SameSite=Strict` stops it from riding along on cross-site
  requests.
- **State-changing requests from a session must also carry
  `X-YAB-CSRF: 1`.** A cross-site form or image can attach a cookie but cannot
  set a custom header, so this double-submit guard blocks the writes even if a
  browser's `SameSite` policy were relaxed.
- **Sessions are in memory only.** Restarting the agent logs everyone out; there
  is no session database to steal from disk. The table is capped at 32 entries
  with least-recently-used eviction, and sessions expire after 12 hours idle.
  An open event stream is closed when its session ends.
- **The UI routes stay behind the CIDR allowlist.** `/ui/login`, `/ui/logout`
  and the assets are pre-authentication, but they are not public: the allowlist
  is checked first on every `/ui/` request, because `provision.sh --no-firewall`
  can leave the port reachable. Pre-authentication state (sessions, one-time
  codes, the per-peer limiter) is bounded so an unauthenticated caller cannot
  grow it without limit, and login rate limiting is per peer so one client
  cannot lock the operator out.
- **The bearer path is untouched.** `/v1/` still accepts
  `Authorization: Bearer <token>` with no CSRF header, so `yab`, the CLI and
  every existing script behave exactly as before.
- **The UI is opt-in.** `web_ui` defaults to `false`; an upgrade never widens an
  existing deployment's attack surface until the operator asks for it.

Rejected alternatives:

- **Token in `localStorage`** - readable by any script, including a future XSS
  in the UI or an injected dependency. It also cannot be marked `HttpOnly`, so
  the token would be exposed in a way the cookie model avoids.
- **Token in a URL query string** - leaks through browser history, `Referer`
  headers and server/proxy logs, and is trivially bookmarked or shared.
- **HTTP Basic auth** - long-lived, sent on every request (including the static
  assets), cannot be logged out without changing the credential, and gives no
  place to hang CSRF or idle expiry.
- **A build step / framework** - directly violates the no-build, stdlib-only
  posture and adds a dependency supply chain for a UI that is a few hundred
  lines of plain JavaScript.

## Consequences

- The credential the browser holds is a short-lived, opaque session id, not the
  API token. Logging out (or restarting the agent) invalidates it.
- `GET /v1/logs` exposes the agent's recent log ring to any authenticated
  caller. Logs can name device paths and bus ids, so this is a new read surface;
  it is behind the same allowlist, token or session as every other route.
- `docs/security.md` governs: all of this still runs over cleartext HTTP. An
  on-path attacker can read the token during login and the session cookie
  afterwards, and can modify responses. The session model reduces token
  exposure, it does not add confidentiality. Put the Pi and PC on a trusted
  segment or a tunnel.
- A future UI for the Windows client reuses the same embedded assets and
  session machinery, so there is one place to audit the browser-side code.
