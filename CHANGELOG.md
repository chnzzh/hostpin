# Changelog

All notable changes to Hostpin are documented here. Hostpin follows semantic
versioning for public releases.

## Unreleased

### Docker distribution

- Added automated Docker Hub publication after a successful GitHub Release,
  including `linux/amd64` and `linux/arm64` manifests, OCI provenance, SBOM,
  semantic version tags, and a pre-push container health check.
- Compose now pulls `chnzzh/hostpin:latest` by default while allowing a pinned
  image through `HOSTPIN_IMAGE`; SQLite remains the default database.
- Added direct image deployment and upgrade instructions in English and
  Chinese.

## 0.1.2 — 2026-09-01

### Explicit public HTTP mode

- Public-IP HTTP server installation is available after a dedicated high-risk
  confirmation; non-interactive automation requires `--allow-insecure-http`.
- The generated configuration records `allow_insecure_http: true` instead of
  silently weakening transport policy.
- Agents present their own second warning before public HTTP enrollment;
  non-interactive Agent enrollment requires `--allow-http`.
- Added English and Chinese guidance for no-domain direct-IP deployment and
  its credential/script interception risks.

## 0.1.1 — 2026-09-01

### Deployment

- The Linux server installer now listens on `0.0.0.0:8080` by default and asks
  for the public URL on first installation.
- Pressing Enter selects the detected private address on port 8080; automation
  can continue to use `--public-url`, and public plain HTTP remains rejected.
- Updated the English and Chinese host deployment documentation.

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
