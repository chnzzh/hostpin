# API and protocol boundary

[简体中文](zh-CN/api.md) · English

Hostpin's native contract is versioned below `/api/v1`. JSON timestamps are
UTC RFC 3339, capacities and counters are bytes, durations are seconds, and
percentages are numeric values from 0 to 100. Nodes use UUID identifiers.

## Authentication

- Enrollment uses the shared PIN exactly once at `POST /api/v1/enrollments`.
  Successful nodes use an independent high-entropy Bearer credential.
- Administrators can create a temporary PIN that defaults to a 30-minute
  lifetime and one successful new-node enrollment. It coexists with the
  permanent PIN and expires after use, time, or explicit revocation.
- Browser administration uses an HttpOnly session cookie plus a CSRF cookie
  and `X-CSRF-Token` header on writes.
- Revocable API keys use `Authorization: Bearer <token>`. v1 keys have the
  `admin` scope and do not change public privacy semantics.
- Share links are read-only, expire, and contain an explicit node allowlist.

Secrets are returned only when created. Databases retain token hashes, not raw
Agent credentials, API keys, session tokens, or share tokens.

## Native routes

| Route | Purpose |
| --- | --- |
| `POST /api/v1/enrollments` | PIN self-enrollment for `monitor` or `probe` role |
| `GET /api/v1/agent/stream` | authenticated Agent WebSocket |
| `POST /api/v1/agent/reports` | authenticated HTTP fallback |
| `GET /api/v1/agent/config` | current structured collection/probe config |
| `GET /api/v1/public/site` | public site identity and privacy state |
| `GET /api/v1/public/nodes[/{id}]` | current visible monitor state |
| `GET /api/v1/public/history` | tier-selected host history |
| `GET /api/v1/public/probes` | redacted service probes; `purpose=carrier` selects carrier latency |
| `GET /api/v1/public/latency` | public Probe Node RTT/loss matrix |
| `GET /api/v1/public/latency/history` | public route history |
| `GET /api/v1/public/live` | public browser WebSocket |
| `GET|POST|DELETE /api/v1/admin/enrollment/temporary-pin` | inspect, create, or revoke a one-use temporary PIN |
| `PUT /api/v1/admin/nodes/{id}/latency` | enable or disable latency measurement on a monitor node |
| `/api/v1/admin/*` | authenticated node, probe, alert, theme, storage and security management |

Administrative backup routes are
`/api/v1/admin/backups/status|export|import`. Export and import require an
administrator session, CSRF, and current-password confirmation. Import accepts
only the encrypted `.hostpin-backup` container, never a plain ZIP.

`POST /api/v1/admin/enrollment/temporary-pin` accepts `expires_in_minutes`
(5–1440, default 30). The plaintext `pin` appears only in the creation
response; later `GET` responses expose only `active|used|expired|revoked`
state and timestamps. Node creation and one-use PIN consumption share one
database transaction, so failed enrollment does not consume the PIN and a
retry with the same `install_id + token` remains idempotent.

Per-node traffic correction uses
`GET|PUT|DELETE /api/v1/admin/nodes/{id}/traffic-correction`. `PUT` accepts target `rx_bytes`
and `tx_bytes` totals in bytes. Responses include raw and displayed totals,
signed adjustments, the active period, and availability. Updates and clears
are both audited.

The node latency-capability route accepts `{"enabled": true|false}`. Enabling
it does not change the node's `monitor` role: host metrics continue normally,
and the same Agent additionally receives latency-matrix tasks. Disabling it
keeps the node and existing latency history.

Error responses use a stable `error.code` and human-readable `error.message`.
Unknown, hidden, expired, and revoked resources do not leak private metadata.

## Agent frames

The WebSocket accepts only typed `hello`, `sample`, and `probe_result` frames.
Server acknowledgements contain acceptance state, server time, structured
collector configuration, and fixed ICMP/TCP/HTTP/DNS tasks. A probe-only node
gets only ICMP/TCP latency tasks. A regular monitor gets service and carrier
measurements, and receives latency-matrix tasks when `latency_enabled` is on.
The protocol has no shell, PTY, arbitrary command, script, log-tail, reverse
tunnel, or administrator-supplied binary URL field.

The normal cadence is a lightweight sample every three seconds and a durable
sample every 60 seconds. HTTP fallback carries the same metric and probe
objects. The server's receive time is authoritative for history while the
Agent's collection time is retained for clock-offset inspection.

## Komari theme adapter

Compatibility routes under `/api`, `/api/rpc2`, and `/api/clients` exist only
for public frontend themes. They translate Hostpin's native model at the HTTP
boundary and are not used by the Hostpin Agent, alert engine, repositories, or
native dashboard. Komari Agent, plugin, management, command, and terminal
contracts are intentionally unsupported. See
[`theme-compatibility.md`](theme-compatibility.md) for the pinned baseline.
