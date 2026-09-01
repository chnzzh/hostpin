# Hostpin

[简体中文](README.zh-CN.md) | English

Hostpin is a self-hosted server monitoring platform with PIN-based, zero-touch
agent enrollment. A node can join directly from the installer without being
created in the dashboard first.

The project contains a compact Go server, a cross-platform Go agent, an
embedded Vue dashboard, SQLite and PostgreSQL storage backends, structured
network probes, notifications, and a Komari-compatible public theme API.

## Status

Hostpin v0.1.0 is the first public release of the production-oriented,
single-instance platform. The native monitoring and enrollment contracts are
versioned from this release; remote shell and arbitrary command execution are
intentionally out of scope.

## Included

- PIN self-enrollment with per-node credentials and idempotent reinstall.
- CPU, load, memory, swap, disk/IO, network/traffic, connections, processes,
  temperature, uptime, platform and optional NVIDIA/AMD GPU collection.
- Three-second live WebSocket telemetry, HTTP fallback, tiered history, fixed
  ICMP/TCP/HTTP/DNS probes, alerts, recovery events and expiry reminders.
- Outbound-only Latency Probe Nodes for public regions, offices, homes and
  private routers, with an RTT/loss matrix and per-route history. Probe Nodes
  work behind NAT or CGNAT and need no public IP or inbound port.
- Any regular monitoring node can also act as a latency measurement point with
  one dashboard switch; its host monitoring and existing history remain intact.
- Built-in Telecom, Unicom, and Mobile latency/loss measurements from every
  regular Agent, with current values and history on each node detail page.
- SMTP, Telegram, Bark and signed generic Webhook delivery with durable retry.
- SQLite WAL and PostgreSQL 16 monthly metric partitions, plus verified offline
  SQLite-to-PostgreSQL migration.
- Encrypted one-click SQLite export/import with integrity validation,
  in-process reload, session revocation, and retained rollback files.
- Responsive Vue operations console, TOTP/recovery codes, session management,
  API keys, private sites, hidden nodes and expiring node-scoped share links.
- Safe Komari theme ZIP installation and public REST/WebSocket/RPC2 theme
  compatibility.

## Quick start

SQLite is the default database. No external database service or database
configuration is required for the following startup path; Hostpin creates
`./data/hostpin.db` automatically.

```sh
git clone https://github.com/chnzzh/hostpin.git
cd hostpin
make build GO=/path/to/go1.26/bin/go
./bin/hostpin-server serve
```

Open `http://localhost:8080/setup`, create the administrator and enrollment
PIN, then install an agent from the command shown in the dashboard.

“Site settings → Node enrollment” can also issue a one-use temporary PIN. It
defaults to 30 minutes and expires after one successful node enrollment, so a
temporary operator does not need the permanent PIN.

```sh
curl -fsSL http://localhost:8080/install.sh | sh
```

Install a latency-only measurement node on a router or regional host:

```sh
curl -fsSL http://localhost:8080/install.sh | sh -s -- --probe-node
```

Uninstall an Agent in one line while preserving its identity for a later
reinstall:

```sh
curl -fsSL http://localhost:8080/uninstall.sh | sh
```

Replace the final `sh` with `sudo sh` for a system-wide installation. Add
`--purge` only when the local Agent identity should also be removed.

Production enrollment should use HTTPS. See `docs/deployment.md` for binary,
Docker Compose, reverse proxy, PostgreSQL, backup, and upgrade guidance.

The default frontend is embedded in `hostpin-server`; runtime does not require
Node.js. [Chinese documentation](docs/zh-CN/README.md) is also available.
Documentation:

- [`deployment.md`](docs/deployment.md): binary, Docker, proxy, backup, upgrade,
  and SQLite-to-PostgreSQL operation;
- [`backup-restore.md`](docs/backup-restore.md): encrypted UI export, verified
  import, automatic reload, and server moves;
- [`architecture.md`](docs/architecture.md): package boundaries, data path, and
  extension rules;
- [`traffic-accounting.md`](docs/traffic-accounting.md): reset periods,
  counter recovery, quota modes, and limitations;
- [`latency-probes.md`](docs/latency-probes.md): per-node carrier latency plus
  public and private NAT-safe measurement nodes;
- [`api.md`](docs/api.md): native authentication, routes, Agent frames, and
  compatibility boundary;
- [`theme-compatibility.md`](docs/theme-compatibility.md): exact Komari theme
  scope;
- [`testing.md`](docs/testing.md): release commands and requirement-to-test
  matrix;
- [`releasing.md`](docs/releasing.md): maintainer signing-key and GitHub Release
  checklist.

Project maintenance files: [`CHANGELOG.md`](CHANGELOG.md),
[`CONTRIBUTING.md`](CONTRIBUTING.md), and [`SECURITY.md`](SECURITY.md).

## Verification

```sh
make test GO=/path/to/go1.26/bin/go
make lint GO=/path/to/go1.26/bin/go
make security GO=/path/to/go1.26/bin/go
python tests/e2e/browser_smoke.py       # against a fresh server on :18082
python tests/e2e/theme_compat.py        # Komari Web, Carbon, and Pulse fixtures
python tests/e2e/backup_restore.py      # against a fresh server on :18084
```

CI repeats the suites on SQLite and PostgreSQL 16, race-checks the stateful
packages, and cross-compiles all 13 supported Agent targets. The theme suite
checks the official Komari Web bundle, Carbon, Pulse, the verified market,
managed configuration, public/private views, REST, RPC2, and both
compatibility WebSockets.

The scheduled capacity workflow drives real WebSocket traffic for 65 seconds
with 100 virtual Agents on SQLite and 1,000 on PostgreSQL. It fails on a
five-second visibility breach, a latest-state API p95 above 300 ms, missing
history points, a degraded persistence queue, or any dropped row. Its PIN is
read from `HOSTPIN_LOAD_PIN`, never from a command-line argument.

## Principles

- Monitoring only: no PTY, web shell, or arbitrary remote execution.
- One PIN enrolls a node once; every node receives an independent credential.
- Public APIs never expose agent tokens, private notes, or unredacted addresses.
- High-frequency live data is kept in memory; durable history is tiered.
- Third-party themes are untrusted code and are installed with explicit review.

## License

MIT
