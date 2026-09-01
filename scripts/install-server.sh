#!/bin/sh
set -eu

repository="chnzzh/hostpin"
public_url="http://127.0.0.1:8080"
listen_address=":8080"
version=""
destination_root="${DESTDIR:-}"

usage() {
  cat <<'EOF'
Install or upgrade Hostpin Server on a Linux systemd host.

Usage:
  install-server.sh [--public-url URL] [--listen ADDRESS] [--version vX.Y.Z]

Options:
  --public-url URL  External origin used in links and Agent installers.
                    Defaults to http://127.0.0.1:8080.
  --listen ADDRESS  HTTP listen address. Defaults to :8080.
  --version VERSION Install a specific GitHub release instead of latest.
  -h, --help        Show this help.

The installer preserves /etc/hostpin/hostpin.yaml and /var/lib/hostpin when
rerun. Set HOSTPIN_RELEASE_BASE to use a trusted alternative HTTPS artifact
directory. DESTDIR is supported for package staging and installer tests.
EOF
}

fail() {
  printf 'hostpin-server installer: %s\n' "$*" >&2
  exit 1
}

require_value() {
  [ "$#" -ge 2 ] && [ -n "$2" ] || fail "$1 requires a value"
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --public-url)
      require_value "$@"
      public_url=$2
      shift 2
      ;;
    --listen)
      require_value "$@"
      listen_address=$2
      shift 2
      ;;
    --version)
      require_value "$@"
      version=$2
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      fail "unknown option: $1"
      ;;
  esac
done

case "$destination_root" in
  ""|/*) ;;
  *) fail "DESTDIR must be an absolute path" ;;
esac

if [ -n "$destination_root" ]; then
  platform="${HOSTPIN_INSTALLER_PLATFORM:-$(uname -s)}"
  machine="${HOSTPIN_INSTALLER_ARCH:-$(uname -m)}"
else
  [ -z "${HOSTPIN_INSTALLER_PLATFORM:-}" ] && [ -z "${HOSTPIN_INSTALLER_ARCH:-}" ] || \
    fail "platform overrides may be used only with DESTDIR"
  platform=$(uname -s)
  machine=$(uname -m)
fi

[ "$platform" = "Linux" ] || fail "only Linux hosts are supported by this server installer"
case "$machine" in
  x86_64|amd64) release_arch=amd64 ;;
  aarch64|arm64) release_arch=arm64 ;;
  *) fail "unsupported architecture: $machine (supported: amd64, arm64)" ;;
esac

if [ -z "$destination_root" ]; then
  [ "$(id -u)" -eq 0 ] || fail "run with root privileges, for example: curl ... | sudo sh"
  command -v systemctl >/dev/null 2>&1 || fail "systemd is required; use the Docker or manual installation instead"
fi

case "$public_url" in
  http://*|https://*) ;;
  *) fail "--public-url must begin with http:// or https://" ;;
esac
if printf '%s\n%s\n' "$public_url" "$listen_address" | grep -q '[[:space:]"\\]'; then
  fail "URL and listen address must not contain whitespace, quotes, or backslashes"
fi
if [ -n "$version" ]; then
  printf '%s\n' "$version" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+([+-][0-9A-Za-z.-]+)?$' || \
    fail "--version must be a semantic version such as v1.2.3"
fi

if [ -n "${HOSTPIN_RELEASE_BASE:-}" ]; then
  release_base=${HOSTPIN_RELEASE_BASE%/}
elif [ -n "$version" ]; then
  release_base="https://github.com/$repository/releases/download/$version"
else
  release_base="https://github.com/$repository/releases/latest/download"
fi
case "$release_base" in
  https://*) ;;
  file://*) [ -n "$destination_root" ] || fail "file:// artifacts require DESTDIR" ;;
  *) fail "release artifacts must be downloaded over HTTPS" ;;
esac

for command_name in awk grep install mktemp; do
  command -v "$command_name" >/dev/null 2>&1 || fail "required command is missing: $command_name"
done

temporary_directory=$(mktemp -d "${TMPDIR:-/tmp}/hostpin-server-install.XXXXXX")
cleanup() {
  rm -rf "$temporary_directory"
}
trap cleanup EXIT HUP INT TERM

artifact="hostpin-server-linux-$release_arch"
download_file() {
  source_url=$1
  output_path=$2
  case "$source_url" in
    file://*)
      cp "${source_url#file://}" "$output_path"
      ;;
    *)
      if command -v curl >/dev/null 2>&1; then
        curl -fL --retry 3 --connect-timeout 10 -o "$output_path" "$source_url"
      elif command -v wget >/dev/null 2>&1; then
        wget -q --https-only -O "$output_path" "$source_url"
      else
        fail "curl or wget is required"
      fi
      ;;
  esac
}

printf 'Downloading Hostpin Server for linux/%s...\n' "$release_arch"
download_file "$release_base/$artifact" "$temporary_directory/$artifact"
download_file "$release_base/$artifact.sha256" "$temporary_directory/$artifact.sha256"

expected_hash=$(awk 'NR == 1 { print $1 }' "$temporary_directory/$artifact.sha256")
case "$expected_hash" in
  ""|*[!0-9A-Fa-f]*) fail "release checksum is malformed" ;;
esac
[ "${#expected_hash}" -eq 64 ] || fail "release checksum is malformed"

if command -v sha256sum >/dev/null 2>&1; then
  actual_hash=$(sha256sum "$temporary_directory/$artifact" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
  actual_hash=$(shasum -a 256 "$temporary_directory/$artifact" | awk '{print $1}')
elif command -v openssl >/dev/null 2>&1; then
  actual_hash=$(openssl dgst -sha256 "$temporary_directory/$artifact" | awk '{print $NF}')
else
  fail "sha256sum, shasum, or openssl is required"
fi
[ "$actual_hash" = "$expected_hash" ] || fail "release checksum verification failed"

binary_path="$destination_root/usr/local/bin/hostpin-server"
config_directory="$destination_root/etc/hostpin"
config_path="$config_directory/hostpin.yaml"
data_directory="$destination_root/var/lib/hostpin"
unit_directory="$destination_root/etc/systemd/system"
unit_path="$unit_directory/hostpin-server.service"

if [ -z "$destination_root" ]; then
  if ! command -v getent >/dev/null 2>&1; then
    fail "getent is required to create the Hostpin service account"
  fi
  if ! getent group hostpin >/dev/null 2>&1; then
    command -v groupadd >/dev/null 2>&1 || fail "groupadd is required to create the Hostpin service account"
    groupadd --system hostpin
  fi
  if ! id -u hostpin >/dev/null 2>&1; then
    command -v useradd >/dev/null 2>&1 || fail "useradd is required to create the Hostpin service account"
    nologin_shell=/usr/sbin/nologin
    [ -x "$nologin_shell" ] || nologin_shell=/sbin/nologin
    [ -x "$nologin_shell" ] || nologin_shell=/bin/false
    useradd --system --gid hostpin --home-dir /var/lib/hostpin --no-create-home --shell "$nologin_shell" hostpin
  fi
  install -d -m 0750 -o hostpin -g hostpin "$data_directory"
  install -d -m 0755 "$config_directory" "$(dirname "$binary_path")" "$unit_directory"
else
  install -d -m 0750 "$data_directory"
  install -d -m 0755 "$config_directory" "$(dirname "$binary_path")" "$unit_directory"
fi

[ ! -L "$binary_path" ] || fail "refusing to replace symlink: $binary_path"
if [ -f "$binary_path" ]; then
  install -m 0755 "$binary_path" "$binary_path.rollback"
fi
install -m 0755 "$temporary_directory/$artifact" "$binary_path.new"
mv -f "$binary_path.new" "$binary_path"

[ ! -L "$config_path" ] || fail "refusing to write through symlink: $config_path"
if [ ! -f "$config_path" ]; then
  cat > "$config_path" <<EOF
listen: "$listen_address"
public_url: "$public_url"
data_dir: "/var/lib/hostpin"
log_level: "info"

database:
  driver: "sqlite"
  dsn: "/var/lib/hostpin/hostpin.db"

security:
  allow_insecure_http: false
  trusted_proxies: ["127.0.0.1/32", "::1/128"]
  allowed_origins: []
  enrollment_cidrs: []

geoip:
  enabled: true
  provider: "https://ipwho.is/{ip}"
  timeout: 4s
  cache_ttl: 720h

runtime:
  persist_queue_size: 10000
  offline_after: 90s
  shutdown_timeout: 15s
EOF
  chmod 0640 "$config_path"
  if [ -z "$destination_root" ]; then
    chown root:hostpin "$config_path"
  fi
else
  printf 'Preserving existing configuration: %s\n' "$config_path"
fi

[ ! -L "$unit_path" ] || fail "refusing to write through symlink: $unit_path"
cat > "$unit_path" <<'EOF'
[Unit]
Description=Hostpin self-hosted monitoring server
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=hostpin
Group=hostpin
ExecStart=/usr/local/bin/hostpin-server serve --config /etc/hostpin/hostpin.yaml
Restart=always
RestartSec=5s
UMask=0027
NoNewPrivileges=true
PrivateTmp=true
ProtectHome=true
ProtectSystem=strict
ReadWritePaths=/var/lib/hostpin
AmbientCapabilities=CAP_NET_BIND_SERVICE

[Install]
WantedBy=multi-user.target
EOF
chmod 0644 "$unit_path"

if [ -n "$destination_root" ]; then
  printf 'Hostpin staged under %s\n' "$destination_root"
  exit 0
fi

systemctl daemon-reload
systemctl enable hostpin-server.service >/dev/null
if ! systemctl restart hostpin-server.service; then
  journalctl -u hostpin-server.service -n 30 --no-pager >&2 || true
  fail "Hostpin service failed to start"
fi
if ! systemctl is-active --quiet hostpin-server.service; then
  journalctl -u hostpin-server.service -n 30 --no-pager >&2 || true
  fail "Hostpin service is not active"
fi

printf '\nHostpin Server is installed.\n'
printf '  Setup:   %s/setup\n' "${public_url%/}"
printf '  Config:  %s\n' "$config_path"
printf '  Data:    %s\n' "$data_directory"
printf '  Status:  systemctl status hostpin-server\n'
if [ "$public_url" = "http://127.0.0.1:8080" ]; then
  printf '  Remote access: configure HTTPS and update public_url before enrolling remote Agents.\n'
fi
