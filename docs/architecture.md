# Hostpin architecture

[简体中文](zh-CN/architecture.md) · English

Hostpin is a single-site monitoring system with two Go binaries and no remote
execution channel.

## Maintainability boundary

`cmd/hostpin-server` is the composition root. Domain packages do not import the
HTTP layer and accept the narrowest repository capability they need. The store
contract is split into lifecycle, identity, settings, node, metric, probe,
alert, theme, and audit interfaces; the SQL implementation composes them for
the server. This keeps alerting, notifications, themes, and maintenance jobs
independently testable and prevents a compatibility adapter from becoming a
second core architecture.

The main package boundaries are:

- `internal/collector`, `agent`, `probe`, `enrollment`, and `service` for the
  low-footprint endpoint runtime;
- `internal/core` for live state, traffic deltas, broadcast, and bounded
  persistence;
- `internal/store` and `store/sqlstore` for versioned storage contracts and
  SQLite/PostgreSQL implementations;
- `internal/alerting` and `notification` for rule state and durable delivery;
- `internal/httpapi` for native transport plus isolated `komari*` adapters;
- `internal/theme` for untrusted archive validation and installed assets;
- `internal/backup` for encrypted site containers, consistent snapshots,
  restore validation, and rollback retention;
- `web/src` for the native Vue operations console.

New databases implement repository capabilities rather than leaking SQL into
handlers. New notification channels belong in the dispatcher. New fixed probe
types require an explicit model, validator, Agent implementation, and tests;
there is deliberately no generic command escape hatch.

## Data path

1. `hostpin-agent install` generates an installation UUID and a random
   per-node credential locally. The shared PIN is sent only to
   `POST /api/v1/enrollments`.
2. The Agent sends compact samples every three seconds over WebSocket. Every
   60 seconds, the server places one sample in the bounded persistence queue.
3. Current state stays in memory and is broadcast to browsers. SQLite uses WAL
   and short batched writes; PostgreSQL stores metrics in monthly partitions.
4. Raw 60-second data rolls into five-minute and one-hour tables before the
   configured retention job removes old points.

The persistence queue is bounded. If storage is unavailable, current state and
browser updates continue in memory while writes retry. When the queue reaches
its bound, the oldest history item is dropped and `/healthz` reports degraded
state and the exact dropped count. See
[`traffic-accounting.md`](traffic-accounting.md) for counter/reset semantics.

## Latency Probe Nodes

A node enrolls with one immutable role: `monitor` or `probe`. Monitoring nodes
collect host telemetry and may run service probes. Probe Nodes skip host metric
collection and run only server-defined ICMP or TCP latency targets. Reusing an
existing installation identity with a different role is rejected.

Probe Nodes always initiate HTTPS or WebSocket connections to Hostpin. The
server never connects back to them, so a router behind NAT or CGNAT does not
need a public address, port forward, firewall exception, or dynamic DNS name.
The same authenticated outbound stream carries heartbeats, structured target
configuration, and multi-sample results containing average RTT and loss.

Latency targets are tied to monitored servers, but their address need not match
the server's detected IP. v1 distributes one shared target address to every
Probe Node. A LAN address works when all participating measurement points can
reach that LAN; a deployment mixing public and home Probe Nodes must use an
address reachable from all of them, or the unreachable points will record a
normal failure. Per-Probe address overrides are outside v1. Target addresses
and raw network errors are administrator-only. Public APIs expose only enabled
public targets, non-hidden Probe Nodes, RTT/loss results, and display metadata.

The Agent protocol contains samples, heartbeats, fixed probe tasks, probe
results, and collector configuration. It has no shell, PTY, command, container,
or log message type.

## Compatibility isolation

Komari support is a presentation adapter, not an upstream fork. Native models,
Agent protocol, storage, alerting, and administration do not depend on Komari.
`internal/httpapi/komari.go`, `komari_rpc.go`, and `komari_metrics.go` translate
only the public theme-facing REST/WebSocket/RPC2 contract. Unsupported Agent,
plugin, management, and terminal calls are not forwarded into Hostpin.

The built-in Vue interface remains the recovery and administration surface even
when an external theme is active. Theme assets are served only from the active
theme's validated `dist/` tree; login, setup, and administration always use the
embedded native application.

## Trust boundaries

- Enrollment PIN hashes, administrator passwords, Agent secrets, API keys, and
  share tokens are never stored in plaintext.
- Notification and TOTP credentials are encrypted with `data/master.key`,
  which is generated with mode `0600` unless supplied through the environment.
- Proxy address headers are ignored unless the direct peer is in
  `security.trusted_proxies`.
- Theme ZIPs are untrusted input. Extraction is bounded and rejects traversal,
  links, duplicate paths, suspicious compression ratios, and invalid manifests.
- Automatic Agent updates are disabled by default and require a manifest
  signed by the Ed25519 public key compiled into the release binary.
- One-click backups use a separate encryption passphrase. Import validates
  every authenticated chunk, ZIP safety, SHA-256, database integrity, and the
  master-key relationship before retaining current data as rollback files.

## Capacity boundary

SQLite is the default for approximately 100 nodes. PostgreSQL 16+ is intended
for approximately 1,000 nodes. Hostpin v1 deliberately runs one server
instance and does not claim multi-replica consistency.

The native frontend lazy-loads route views and ECharts so the overview does not
pay the charting bundle cost. Its design tokens support dark, light, and system
appearance, and charts observe appearance changes without a reload. The
industrial telemetry layout favors dense comparison, explicit units, keyboard
focus, semantic status, and WCAG AA text contrast.
