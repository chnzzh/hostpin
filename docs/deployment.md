# Deployment

[简体中文](zh-CN/deployment.md) · English

Commands using `deploy/...` and Docker below assume the current directory is
the Hostpin source repository root. Obtain `hostpin-server` for the matching
platform from a release artifact, or build it from source with Go 1.26.x and
the locked pnpm dependencies:

```sh
make build GO=/path/to/go1.26/bin/go
```

The resulting binaries are `bin/hostpin-server` and `bin/hostpin-agent`. The
examples below use the source-build path; replace it with the downloaded
artifact path when using a release binary.

## Single binary with SQLite

Create a dedicated user and directories, install
`deploy/hostpin-server.service`, and copy `deploy/hostpin.example.yaml` to
`/etc/hostpin/hostpin.yaml`. The service needs write access only to
`/var/lib/hostpin`.

Put Hostpin behind an HTTPS reverse proxy and set `public_url` to the external
origin. Add only the proxy's actual network to `security.trusted_proxies`.
Hostpin ignores forwarded client-IP headers from every other peer.

```sh
sudo install -m 0755 ./bin/hostpin-server /usr/local/bin/hostpin-server
sudo install -m 0644 deploy/hostpin-server.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now hostpin-server
```

## Docker Compose

SQLite:

```sh
HOSTPIN_PUBLIC_URL=https://monitor.example.com \
  docker compose -f deploy/docker-compose.sqlite.yml up -d --build
```

PostgreSQL 16:

```sh
POSTGRES_PASSWORD='replace-this' \
HOSTPIN_PUBLIC_URL=https://monitor.example.com \
  docker compose -f deploy/docker-compose.postgres.yml up -d --build
```

## Agent enrollment

By default, installers download Agent artifacts from the official GitHub
release. A self-hosted deployment can set `agent_release_base` (or
`HOSTPIN_AGENT_RELEASE_BASE`) to an HTTPS directory containing each
`hostpin-agent-<os>-<arch>` artifact and its `.sha256` sidecar.

Unix-like systems:

```sh
curl -fsSL https://monitor.example.com/install.sh | sh
```

The script detects the platform, downloads the matching release and verifies
its SHA-256 sidecar. The Agent itself opens `/dev/tty` and asks for the PIN,
name, group, region, and tags. Add `--advanced` for billing, traffic, interface,
mountpoint, GPU, visibility, and update settings. The monthly traffic quota is
entered in GiB; `0` means unlimited.

Interactive questions have no network deadline. A 65-second network window
starts only after every answer has been collected and the Agent is ready to
send the enrollment request. Transient failures reuse the same `install_id`
and per-node token. That pending identity is stored with mode `0600` without
the PIN, so rerunning the installer remains idempotent if a response was lost.

Windows users download `/install.ps1`, inspect it, and execute it in an
elevated PowerShell session. PIN values are never accepted as command-line
arguments. Automation may use `HOSTPIN_PIN` in a restricted environment or a
mode-`0600` PIN file.

## Latency measurement nodes

Install an outbound-only Probe Node on Linux, OpenWrt, a NAS, macOS, or
FreeBSD:

```sh
curl -fsSL https://monitor.example.com/install.sh | sh -s -- --probe-node
```

On Windows, download and inspect the script before executing it:

```powershell
Invoke-WebRequest -UseBasicParsing 'https://monitor.example.com/install.ps1' -OutFile .\hostpin-install.ps1
.\hostpin-install.ps1 -ProbeNode
```

The Agent asks for the enrollment PIN and measurement-point metadata, then
opens an outbound WSS/HTTPS connection. No listening socket, public IP, port
forward, or inbound firewall rule is needed. In the administration console,
open **Latency nodes**, add each monitored server as an ICMP or TCP target, and
choose whether the measurement point and target are public.

An existing regular Agent can also measure the latency matrix: edit it in
**Admin → Nodes** and enable **Use as a latency measurement point**. It keeps
collecting full host metrics and does not need to be reinstalled.

ICMP requires the platform's `ping` utility and may be restricted by local
permissions or upstream networks. TCP measurement (for example `host:443`) is
the portable fallback and measures connect time rather than ICMP echo time.
See `docs/latency-probes.md` for visibility and routing details.

## One-line Agent uninstall

Run as the same user that installed the Agent:

```sh
curl -fsSL https://monitor.example.com/uninstall.sh | sh
```

Use `sudo sh` for a system-wide installation. By default the service and binary
are removed while `agent.json` is retained so a reinstall can resume the same
node identity. Add `--purge` only to remove that local identity as well:

```sh
curl -fsSL https://monitor.example.com/uninstall.sh | sudo sh -s -- --purge
```

Use `--dry-run` to inspect actions without changing the host. In an
Administrator PowerShell window on Windows:

```powershell
irm https://monitor.example.com/uninstall.ps1 | iex
```

Uninstalling does not delete the node or its history from the panel. Delete the
offline node in **Nodes** only when that history is no longer needed.

Plain HTTP enrollment is rejected for public addresses. Loopback and private
addresses still require an interactive confirmation or `--allow-http`.
The server likewise refuses a public-address `http://` `public_url` by default.
`HOSTPIN_ALLOW_INSECURE_HTTP=true` is an explicit legacy escape hatch and
should not be used for an Internet-facing deployment.

## SQLite to PostgreSQL

Stop the Hostpin server, create an empty PostgreSQL database, then run:

```sh
hostpin-server migrate sqlite-to-postgres \
  --source /var/lib/hostpin/hostpin.db \
  --target 'postgres://hostpin:password@db:5432/hostpin?sslmode=require'
```

The command copies each table, creates all required metric partitions, and
checks row counts and metric/event time ranges. It never deletes the SQLite
source. Keep the source and its WAL files until the PostgreSQL deployment has
been inspected and backed up.

## Backup and recovery

SQLite sites can use **Admin → Backup & restore** for an encrypted complete
export and verified import with an automatic in-process reload. See
[`backup-restore.md`](backup-restore.md) for its validation and rollback model.
The manual process below remains useful for offline maintenance.

For SQLite, stop Hostpin or use SQLite's online backup tooling and copy the
database together with `master.key` and the `themes/` directory. For
PostgreSQL, use `pg_dump --format=custom`; separately back up `master.key` and
themes. Losing `master.key` makes existing TOTP and notification credentials
unrecoverable but does not expose them.

Before every upgrade, preserve the database, data directory, and previous
binaries. Agent updates retain a `.rollback` binary beside the executable.
