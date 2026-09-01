# Changelog

All notable changes to Hostpin are documented here. Hostpin follows semantic
versioning for public releases.

## 0.1.0 — 2026-08-31

First public release.

### Monitoring and enrollment

- PIN-based self-enrollment with independent per-node credentials, idempotent
  reinstall, one-use temporary PINs, and Linux/Windows installers.
- Cross-platform Agent collection for CPU, load, memory, swap, disk/IO,
  network and monthly traffic, connections, processes, temperature, uptime,
  platform identity, and optional GPU metrics.
- Three-second live telemetry, HTTP fallback, tiered history, traffic
  correction, GiB quotas, expiry and billing metadata.

### Latency, probes, and alerts

- Structured ICMP, TCP, HTTP(S), and DNS probes with no shell or arbitrary
  command channel.
- Built-in Telecom, Unicom, and Mobile latency/loss history on node details.
- NAT-safe probe-only measurement nodes and a public/private latency matrix.
- Regular monitoring nodes can also act as latency measurement points without
  reinstalling the Agent.
- Alert state machine with recovery events and SMTP, Telegram, Bark, and
  HMAC-signed Webhook delivery.

### Storage, access, and themes

- SQLite by default, PostgreSQL 16+ support, tiered retention, and verified
  offline SQLite-to-PostgreSQL migration.
- Checksum-verified one-command Linux systemd server installation and upgrade,
  plus documented SQLite and PostgreSQL Docker Compose deployment paths.
- Encrypted one-click backup/export and verified restore/import with rollback.
- Single-administrator sessions, TOTP, recovery codes, API keys, private
  sites, hidden nodes, and expiring node-scoped share links.
- Native responsive dashboard plus the pinned Komari public-theme compatibility
  layer, including safe theme ZIP installation.

### Release boundary

- Single site, single administrator, and one server instance.
- No multi-tenancy, RBAC, high availability, container monitoring, log
  collection, web terminal, PTY, or arbitrary remote command execution.
