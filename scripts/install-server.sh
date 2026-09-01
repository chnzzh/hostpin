#!/bin/sh
set -eu

repository="chnzzh/hostpin"
public_url=""
public_url_set=0
allow_insecure_http=0
allow_insecure_http_set=0
listen_address="0.0.0.0:8080"
version=""
destination_root="${DESTDIR:-}"

usage() {
  cat <<'EOF'
Install or upgrade Hostpin Server on a Linux systemd host.

Usage:
  install-server.sh [--public-url URL] [--listen ADDRESS] [--version vX.Y.Z]

Options:
  --public-url URL  External origin used in links and Agent installers.
                    When omitted, the installer asks interactively. Pressing
                    Enter uses the detected private address on port 8080.
  --listen ADDRESS  HTTP listen address. Defaults to 0.0.0.0:8080.
  --allow-insecure-http
                    Permit a public plain-HTTP URL without an interactive
                    warning. High risk; intended only for explicit automation.
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

is_private_ipv4() {
  case "$1" in
    10.*|127.*|192.168.*|172.1[6-9].*|172.2[0-9].*|172.3[01].*) return 0 ;;
    *) return 1 ;;
  esac
}

is_safe_plain_http_host() {
  normalized_http_host=$(printf '%s\n' "$1" | awk '{ print tolower($0) }')
  if is_private_ipv4 "$normalized_http_host"; then
    return 0
  fi
  case "$normalized_http_host" in
    localhost|::1|fc[0-9a-f][0-9a-f]:*|fd[0-9a-f][0-9a-f]:*) return 0 ;;
    *) return 1 ;;
  esac
}

detect_default_host() {
  if [ -n "${HOSTPIN_INSTALLER_DEFAULT_HOST:-}" ]; then
    [ -n "$destination_root" ] || fail "HOSTPIN_INSTALLER_DEFAULT_HOST may be used only with DESTDIR"
    printf '%s\n' "$HOSTPIN_INSTALLER_DEFAULT_HOST"
    return
  fi
  detected_host=""
  if command -v ip >/dev/null 2>&1; then
    detected_host=$(ip -4 route get 1.1.1.1 2>/dev/null | awk '{ for (i = 1; i <= NF; i++) if ($i == "src") { print $(i + 1); exit } }' || true)
    if is_private_ipv4 "$detected_host"; then
      printf '%s\n' "$detected_host"
      return
    fi
  fi
  if command -v hostname >/dev/null 2>&1; then
    for detected_host in $(hostname -I 2>/dev/null || true); do
      if is_private_ipv4 "$detected_host"; then
        printf '%s\n' "$detected_host"
        return
      fi
    done
  fi
  printf '127.0.0.1\n'
}

read_config_public_url() {
  awk '$1 == "public_url:" { sub(/^[^:]*:[[:space:]]*/, ""); gsub(/^"|"$/, ""); print; exit }' "$1"
}

read_config_allow_insecure_http() {
  awk '$1 == "allow_insecure_http:" { print tolower($2); exit }' "$1"
}

confirm_public_plain_http() {
  printf '\nWARNING: this public HTTP URL has no transport encryption.\n' > /dev/tty
  printf 'Administrator passwords, enrollment PINs, sessions, and Agent tokens can be intercepted.\n' > /dev/tty
  printf 'Continue and enable insecure public HTTP? (y/N): ' > /dev/tty
  insecure_http_answer=""
  if IFS= read -r insecure_http_answer < /dev/tty; then :; fi
  insecure_http_answer=$(printf '%s\n' "$insecure_http_answer" | awk '{ print tolower($0) }')
  case "$insecure_http_answer" in
    y|yes|是) return 0 ;;
    *) return 1 ;;
  esac
}

validate_public_url() {
  public_http_requires_confirmation=0
  case "$public_url" in
    http://*) url_remainder=${public_url#http://}; url_scheme=http ;;
    https://*) url_remainder=${public_url#https://}; url_scheme=https ;;
    *) fail "public URL must begin with http:// or https://" ;;
  esac
  case "$url_remainder" in
    */) url_authority=${url_remainder%/} ;;
    */*) fail "public URL must not contain a path" ;;
    *) url_authority=$url_remainder ;;
  esac
  [ -n "$url_authority" ] || fail "public URL must include a host"
  case "$url_authority" in
    *'@'*|*'?'*|*'#'*) fail "public URL must not contain credentials, a query, or a fragment" ;;
  esac
  if [ "$url_scheme" = http ]; then
    case "$url_authority" in
      \[*\]*) url_host=${url_authority#\[}; url_host=${url_host%%\]*} ;;
      *) url_host=${url_authority%%:*} ;;
    esac
    if ! is_safe_plain_http_host "$url_host"; then
      public_http_requires_confirmation=1
    fi
  fi
}

require_value() {
  [ "$#" -ge 2 ] && [ -n "$2" ] || fail "$1 requires a value"
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --public-url)
      require_value "$@"
      public_url=$2
      public_url_set=1
      shift 2
      ;;
    --listen)
      require_value "$@"
      listen_address=$2
      shift 2
      ;;
    --allow-insecure-http)
      allow_insecure_http=1
      allow_insecure_http_set=1
      shift
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

binary_path="$destination_root/usr/local/bin/hostpin-server"
config_directory="$destination_root/etc/hostpin"
config_path="$config_directory/hostpin.yaml"
data_directory="$destination_root/var/lib/hostpin"
unit_directory="$destination_root/etc/systemd/system"
unit_path="$unit_directory/hostpin-server.service"
[ ! -L "$config_path" ] || fail "refusing to read or write through symlink: $config_path"

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

for command_name in awk grep install mktemp; do
  command -v "$command_name" >/dev/null 2>&1 || fail "required command is missing: $command_name"
done

config_exists=0
requested_public_url=$public_url
if [ -f "$config_path" ]; then
  config_exists=1
  preserved_public_url=$(read_config_public_url "$config_path")
  if [ -n "$preserved_public_url" ]; then
    if [ "$public_url_set" -eq 1 ] && [ "$preserved_public_url" != "$requested_public_url" ]; then
      printf 'Ignoring --public-url because the existing configuration is preserved; edit the file to change it.\n'
    fi
    public_url=$preserved_public_url
  fi
  preserved_allow_insecure_http=$(read_config_allow_insecure_http "$config_path")
  if [ "$preserved_allow_insecure_http" = true ]; then
    allow_insecure_http=1
  else
    allow_insecure_http=0
  fi
fi
if [ -z "$public_url" ]; then
  default_host=$(detect_default_host)
  suggested_public_url="http://$default_host:8080"
  if [ -n "${HOSTPIN_INSTALLER_ACCEPT_DEFAULT:-}" ]; then
    [ -n "$destination_root" ] || fail "HOSTPIN_INSTALLER_ACCEPT_DEFAULT may be used only with DESTDIR"
    public_url=$suggested_public_url
  elif [ -z "${HOSTPIN_NONINTERACTIVE:-}" ] && [ -r /dev/tty ] && [ -w /dev/tty ]; then
    printf 'Public URL (HTTPS recommended) [%s]: ' "$suggested_public_url" > /dev/tty
    public_url_answer=""
    if IFS= read -r public_url_answer < /dev/tty; then :; fi
    public_url=${public_url_answer:-$suggested_public_url}
  else
    fail "--public-url is required when no interactive terminal is available"
  fi
fi

if printf '%s\n%s\n' "$public_url" "$listen_address" | grep -q '[[:space:]"\\]'; then
  fail "URL and listen address must not contain whitespace, quotes, or backslashes"
fi
validate_public_url
if [ "$public_http_requires_confirmation" -eq 1 ]; then
  if [ "$config_exists" -eq 1 ]; then
    [ "$allow_insecure_http" -eq 1 ] || \
      fail "the preserved public HTTP configuration must set security.allow_insecure_http to true"
  elif [ "$allow_insecure_http_set" -eq 1 ]; then
    allow_insecure_http=1
  elif [ -z "${HOSTPIN_NONINTERACTIVE:-}" ] && [ -r /dev/tty ] && [ -w /dev/tty ]; then
    confirm_public_plain_http || fail "public HTTP installation was not confirmed"
    allow_insecure_http=1
  else
    fail "public HTTP requires interactive confirmation or --allow-insecure-http"
  fi
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

if [ ! -f "$config_path" ]; then
  allow_insecure_http_yaml=false
  if [ "$allow_insecure_http" -eq 1 ]; then
    allow_insecure_http_yaml=true
  fi
  cat > "$config_path" <<EOF
listen: "$listen_address"
public_url: "$public_url"
data_dir: "/var/lib/hostpin"
log_level: "info"

database:
  driver: "sqlite"
  dsn: "/var/lib/hostpin/hostpin.db"

security:
  allow_insecure_http: $allow_insecure_http_yaml
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
  preserved_public_url=$(read_config_public_url "$config_path")
  if [ -n "$preserved_public_url" ]; then
    public_url=$preserved_public_url
  fi
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
case "$public_url" in
  http://*)
    if [ "$allow_insecure_http" -eq 1 ]; then
      printf '  WARNING: public plain HTTP is enabled; credentials and Agent tokens are not encrypted.\n'
    else
      printf '  Note: plain HTTP is intended for localhost/private networks.\n'
    fi
    ;;
esac
