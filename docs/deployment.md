# Deployment

[简体中文](zh-CN/deployment.md) · English

Hostpin uses SQLite by default. The first start applies migrations and creates
the database automatically; PostgreSQL is needed only when explicitly
selected.

## One-command Linux host install

The release installer supports `amd64` and `arm64` Linux hosts running systemd:

```sh
curl -fsSL https://github.com/chnzzh/hostpin/releases/latest/download/install-server.sh | sudo sh
```

It performs the following operations:

- downloads the matching `hostpin-server` release and verifies its SHA-256
  sidecar before installation;
- creates the restricted `hostpin` user and systemd unit;
- writes a SQLite configuration to `/etc/hostpin/hostpin.yaml`;
- stores durable state in `/var/lib/hostpin` and starts the service.

On a fresh installation it prompts for the public URL through `/dev/tty`. The
suggested value is `http://<detected-private-address>:8080`; pressing Enter
accepts it. The default listener is `0.0.0.0:8080`, so it is reachable from the
host's networks when the firewall permits that port. HTTPS is recommended for
Internet-facing deployments, but a public plain-HTTP URL can be enabled after
a separate high-risk confirmation.

The installer preserves the existing configuration and data directory when it
is rerun. It saves the previous executable as
`/usr/local/bin/hostpin-server.rollback`. To install a specific release, add
`--version v0.1.2`. Existing installations read the URL from their preserved
configuration and do not ask again.

Automation can provide the URL without a prompt:

```sh
curl -fsSL https://github.com/chnzzh/hostpin/releases/latest/download/install-server.sh \
  | sudo sh -s -- --public-url https://monitor.example.com
```

For explicitly accepted, non-interactive public HTTP deployment:

```sh
curl -fsSL https://github.com/chnzzh/hostpin/releases/latest/download/install-server.sh \
  | sudo sh -s -- --public-url http://203.0.113.20:8080 \
      --allow-insecure-http
```

Without `--allow-insecure-http`, the same public HTTP URL requires an
interactive confirmation. The generated configuration records
`security.allow_insecure_http: true`; the setting is therefore visible and
auditable rather than an implicit downgrade.

When a reverse proxy on the same host is the only intended client, add
`--listen 127.0.0.1:8080` to avoid exposing port 8080 directly.

Useful maintenance commands:

```sh
sudo systemctl status hostpin-server
sudo journalctl -u hostpin-server -f
sudo systemctl restart hostpin-server
sudoedit /etc/hostpin/hostpin.yaml
```

The script does not modify DNS, firewall rules, TLS certificates, or reverse
proxy configuration. Point the public hostname at the server and proxy it to
Hostpin before enrolling Internet-facing Agents. A minimal Caddy site is:

```caddyfile
monitor.example.com {
    reverse_proxy 127.0.0.1:8080
}
```

Enter that exact external HTTPS origin at the installer prompt, or pass it with
`--public-url`. After startup, open `https://monitor.example.com/setup`.

### Direct public IP without a domain

At the public URL prompt, enter `http://<public-ip>:8080`. The installer shows
a warning that administrator passwords, enrollment PINs, sessions, and Agent
tokens can be intercepted, then requires an explicit yes. No certificate is
required after accepting that risk.

When an Agent is installed from this panel, it presents a second warning before
enrollment. `--allow-http` is the explicit non-interactive Agent override. Be
aware that downloading and piping any script over public HTTP can itself be
modified in transit; inspect the script or use HTTPS whenever possible.

No-domain HTTPS is also possible with a publicly trusted IP-address
certificate. [Let's Encrypt supports short-lived IPv4/IPv6 certificates](https://letsencrypt.org/2026/01/15/6day-and-ip-general-availability),
but they require automated renewal and are not provisioned by Hostpin's
installer.

## Docker Compose

Published Docker Hub images contain the server and embedded Vue frontend for
`linux/amd64` and `linux/arm64`. Go, Node.js, pnpm, and a source checkout are
not required on the host. The container runs as UID/GID `10001` and stores all
mutable Hostpin data under `/var/lib/hostpin`.

```sh
docker pull chnzzh/hostpin:latest
```

SQLite (recommended for up to about 100 nodes):

```sh
mkdir hostpin && cd hostpin
curl -fsSLo compose.yml https://raw.githubusercontent.com/chnzzh/hostpin/main/deploy/docker-compose.sqlite.yml
HOSTPIN_PUBLIC_URL=https://monitor.example.com \
  docker compose -f compose.yml up -d
```

The named `hostpin-data` volume contains the SQLite database, master key, and
themes. `docker compose ... down` keeps that volume; do not add `--volumes`
unless the site data is intentionally being deleted.

For direct public-IP HTTP, the same explicit risk acceptance used by the
binary installer is required:

```sh
HOSTPIN_PUBLIC_URL=http://203.0.113.20:8080 \
HOSTPIN_ALLOW_INSECURE_HTTP=true \
  docker compose -f compose.yml up -d
```

This mode does not require a domain or certificate, but credentials, PINs,
sessions, and Agent tokens are not encrypted. HTTPS remains recommended.

PostgreSQL 16 (optional for larger single-instance sites):

```sh
mkdir hostpin-postgres && cd hostpin-postgres
curl -fsSLo compose.yml https://raw.githubusercontent.com/chnzzh/hostpin/main/deploy/docker-compose.postgres.yml
POSTGRES_PASSWORD='replace-with-a-long-random-password' \
HOSTPIN_PUBLIC_URL=https://monitor.example.com \
  docker compose -f compose.yml up -d
```

PostgreSQL data is stored in `postgres-data`; Hostpin's master key and themes
remain in `hostpin-data`. This mode is still a single Hostpin instance and does
not add HA or Redis.

To inspect or update a Compose deployment:

```sh
docker compose -f compose.yml ps
docker compose -f compose.yml logs -f hostpin
docker compose -f compose.yml pull
docker compose -f compose.yml up -d
```

Compose uses `chnzzh/hostpin:latest` by default. To pin or test a specific
image, set a complete reference such as
`HOSTPIN_IMAGE=chnzzh/hostpin:v0.1.2`. A local source build remains possible:

```sh
git clone https://github.com/chnzzh/hostpin.git
cd hostpin
docker build -t hostpin:local .
HOSTPIN_IMAGE=hostpin:local \
  docker compose -f deploy/docker-compose.sqlite.yml up -d
```

## Manual binary or source installation

Download the matching `hostpin-server-linux-*` release plus its `.sha256`
sidecar and verify it, or build from source with Go 1.26.x and the locked pnpm
dependencies. Commands using `deploy/...` below assume the current directory
is the Hostpin source repository root:

```sh
make build GO=/path/to/go1.26/bin/go
```

The resulting binaries are `bin/hostpin-server` and `bin/hostpin-agent`. The
examples below use the source-build path; replace it with the downloaded
artifact path when using a release binary.

### Single binary with SQLite

Create a dedicated user and directories, install
`deploy/hostpin-server.service`, and copy `deploy/hostpin.example.yaml` to
`/etc/hostpin/hostpin.yaml`. The service needs write access only to
`/var/lib/hostpin`.

```sh
getent group hostpin >/dev/null || sudo groupadd --system hostpin
id -u hostpin >/dev/null 2>&1 || sudo useradd --system --gid hostpin \
  --home-dir /var/lib/hostpin --no-create-home --shell /usr/sbin/nologin hostpin
sudo install -d -m 0750 -o hostpin -g hostpin /var/lib/hostpin
sudo install -d -m 0755 /etc/hostpin
sudo install -m 0640 -o root -g hostpin deploy/hostpin.example.yaml \
  /etc/hostpin/hostpin.yaml
sudoedit /etc/hostpin/hostpin.yaml
```

Put Hostpin behind an HTTPS reverse proxy and set `public_url` to the external
origin. Add only the proxy's actual network to `security.trusted_proxies`.
Hostpin ignores forwarded client-IP headers from every other peer.

```sh
sudo install -m 0755 ./bin/hostpin-server /usr/local/bin/hostpin-server
sudo install -m 0644 deploy/hostpin-server.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now hostpin-server
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

Every plain-HTTP Agent enrollment requires an interactive confirmation or
`--allow-http`. Public addresses receive a stronger interception warning. The
server installer likewise requires a separate confirmation for a public HTTP
URL, or `--allow-insecure-http` in non-interactive automation, and records the
choice as `security.allow_insecure_http: true`.

`HOSTPIN_ALLOW_INSECURE_HTTP=true` is the equivalent manual configuration
override. HTTPS remains strongly recommended because public HTTP exposes
credentials and telemetry to interception.

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
