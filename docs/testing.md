# Testing and release verification

[简体中文](zh-CN/testing.md) · English

Hostpin uses layered verification so protocol, storage, browser behavior, theme
compatibility, resource usage, and target-platform builds fail independently.

## Local fast path

Use Go 1.26.x and the locked pnpm dependencies:

```sh
make test GO=/path/to/go1.26/bin/go
make lint GO=/path/to/go1.26/bin/go
make security GO=/path/to/go1.26/bin/go
make build GO=/path/to/go1.26/bin/go
```

`make test` runs all Go tests with SQLite and all frontend unit tests. A
`make security` run checks reachable Go vulnerabilities and audits the locked
frontend dependency graph against the official npm advisory endpoint. A
PostgreSQL DSN enables the same integration helpers against PostgreSQL 16+:

```sh
HOSTPIN_TEST_POSTGRES='postgres://hostpin:password@127.0.0.1:5432/hostpin_test?sslmode=disable' \
  /path/to/go1.26/bin/go test ./internal/store/sqlstore
```

Stateful race checks use:

```sh
/path/to/go1.26/bin/go test -race \
  ./internal/core ./internal/httpapi ./internal/security \
  ./internal/alerting ./internal/notification ./internal/store/sqlstore \
  ./internal/backup
```

## Browser and themes

Build both binaries. Browser smoke and theme compatibility share a fresh server
on `127.0.0.1:18082`; backup/restore and carrier latency each use a separate
fresh server on `:18084` and `:18085` respectively:

```sh
python3 -m pip install -r tests/e2e/requirements.txt
python3 -m playwright install chromium
python3 tests/e2e/browser_smoke.py
python3 tests/e2e/carrier_latency.py
python3 tests/e2e/theme_compat.py
python3 tests/e2e/backup_restore.py
```

The browser smoke test performs setup, monitor and Probe Node enrollment,
authenticated reporting, traffic accounting, service and latency probes,
dynamic Agent configuration, offline detection, mobile layouts, alert CRUD,
CSRF rejection, TOTP and one-time recovery login, session/API-key revocation,
private-site access, and expiring node-scoped shares.

The theme suite requires the three pinned official ZIPs and SHA-256 values from
`.github/workflows/ci.yml`. It uploads and activates Komari Web, Carbon, and
Pulse; checks managed settings, public and private pages, node/history/Ping
views, REST, batched RPC2, RPC2 WebSocket, and `/api/clients` live updates.

## Capacity and Agent resources

`.github/workflows/capacity.yml` runs 65 seconds of real authenticated
WebSocket reporting from 100 SQLite Agents and 1,000 PostgreSQL Agents. It
fails if visibility exceeds five seconds, latest-state API p95 exceeds 300 ms,
history is missing, traffic fails to accumulate, persistence degrades, or a
queued row is dropped.

`tests/e2e/linux_smoke.sh` serves a release fixture, downloads the panel's real
`install.sh`, verifies its SHA-256 path for both Monitor and Probe roles, then
enforces WebSocket reporting, API latency, and the v1 Agent resource budgets.
HTTP fallback and dynamic configuration are exercised deterministically in
`internal/agent/runtime_test.go`. Probe-only mode is measured separately
because it does not initialize host collectors.

## Feature-to-test matrix

| Boundary | Authoritative coverage |
| --- | --- |
| PIN limits, Argon2id, global breaker, trusted proxy | `internal/security`, `internal/httpapi/validation_test.go`, browser CSRF check |
| Enrollment identity and credential idempotency | SQLite/PostgreSQL integration, `internal/enrollment`, browser install |
| Unix installer download, checksum, and argument forwarding | installer unit tests and Linux smoke through the served `install.sh` |
| One-line uninstall and identity retention boundary | `uninstall.sh` syntax, dangerous-pattern rejection, and Linux `--dry-run` smoke |
| CPU/memory/disk/network/GPU collection | `internal/collector`, platform CI, Linux smoke |
| Live WebSocket and HTTP fallback | `internal/agent` fallback tests; browser smoke, Linux smoke, and capacity load for live WebSockets |
| Traffic and reset semantics | `internal/core/traffic_test.go`, both SQL integrations, browser smoke, capacity load |
| History, rollup, retention, queue degradation | SQL integrations, `internal/core/persister_test.go`, capacity load |
| ICMP/TCP/HTTP/DNS probes | `internal/probe`, browser service probe |
| Carrier latency/loss and node detail | HTTP privacy test and `carrier_latency.py` desktop/mobile flow |
| NAT-safe latency Probe Nodes | SQLite/PostgreSQL integration, browser latency matrix/history |
| Monitor node also acting as a latency point | SQL dispatch/migration tests, HTTP capability test, browser live-result flow |
| Alert duration/recovery/cooldown/offline | `internal/alerting/engine_test.go`, alert SQL integration, browser CRUD |
| SMTP/Telegram/Bark/Webhook and retry | `internal/notification`, alert SQL integration |
| Sessions, TOTP, recovery, API keys, shares | security/store tests and browser access-control flow |
| Hidden/private redaction | `internal/httpapi/privacy_test.go`, browser private/share flow |
| Theme ZIP safety and compatibility | `internal/theme`, HTTP privacy tests, official theme browser suite |
| SQLite to PostgreSQL conversion | both SQL integrations and transfer verification |
| Encrypted backup, restore, rollback, and live reload | `internal/backup`, HTTP handler tests, and `backup_restore.py` |
| Supported Agent targets | `cross-agent` and `agent-platform` CI jobs |

## Release gate

A stable release requires unit/integration tests, race checks, vet, frontend
typecheck/build/tests, dependency vulnerability scans, browser and
official-theme suites, all 13 Agent cross builds, Linux Agent resource checks,
and scheduled capacity results to pass. Server logs are scanned for panic,
fatal, and error-level failures.

The Tag-triggered workflow and one-time Ed25519 signing setup are documented in
[`releasing.md`](releasing.md).
