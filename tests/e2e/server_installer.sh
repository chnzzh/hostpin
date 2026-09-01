#!/usr/bin/env bash
set -euo pipefail

project_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
test_root="$(mktemp -d "${TMPDIR:-/tmp}/hostpin-server-installer-test.XXXXXX")"
trap 'rm -rf "$test_root"' EXIT

release_dir="$test_root/release"
stage_dir="$test_root/stage"
mkdir -p "$release_dir"
artifact="$release_dir/hostpin-server-linux-amd64"

hash_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

write_fixture() {
  printf '%s\n' "$1" > "$artifact"
  printf '%s\n' "$(hash_file "$artifact")" > "$artifact.sha256"
}

run_installer() {
  DESTDIR="$stage_dir" \
  HOSTPIN_INSTALLER_PLATFORM=Linux \
  HOSTPIN_INSTALLER_ARCH=x86_64 \
  HOSTPIN_INSTALLER_DEFAULT_HOST="${HOSTPIN_INSTALLER_DEFAULT_HOST:-}" \
  HOSTPIN_INSTALLER_ACCEPT_DEFAULT="${HOSTPIN_INSTALLER_ACCEPT_DEFAULT:-}" \
  HOSTPIN_RELEASE_BASE="file://$release_dir" \
    sh "$project_root/scripts/install-server.sh" "$@"
}

write_fixture "first Hostpin server"
HOSTPIN_INSTALLER_DEFAULT_HOST=192.168.50.20 \
HOSTPIN_INSTALLER_ACCEPT_DEFAULT=1 \
  run_installer

installed="$stage_dir/usr/local/bin/hostpin-server"
config="$stage_dir/etc/hostpin/hostpin.yaml"
unit="$stage_dir/etc/systemd/system/hostpin-server.service"
cmp "$artifact" "$installed"
grep -F 'public_url: "http://192.168.50.20:8080"' "$config" >/dev/null
grep -F 'listen: "0.0.0.0:8080"' "$config" >/dev/null
grep -F 'driver: "sqlite"' "$config" >/dev/null
grep -F 'trusted_proxies: ["127.0.0.1/32", "::1/128"]' "$config" >/dev/null
grep -F 'ExecStart=/usr/local/bin/hostpin-server serve --config /etc/hostpin/hostpin.yaml' "$unit" >/dev/null

printf '\n# preserve-me\n' >> "$config"
cp "$installed" "$test_root/first-installed"
write_fixture "second Hostpin server"
run_installer
cmp "$artifact" "$installed"
cmp "$test_root/first-installed" "$installed.rollback"
grep -F '# preserve-me' "$config" >/dev/null

printf '%064d\n' 0 > "$artifact.sha256"
if run_installer >/dev/null 2>&1; then
  echo "installer accepted an invalid checksum" >&2
  exit 1
fi

if run_installer --version v1beta.2.3 >/dev/null 2>&1; then
  echo "installer accepted an invalid semantic version" >&2
  exit 1
fi

write_fixture "third Hostpin server"
public_http_error="$(run_installer --public-url http://198.51.100.20:8080 2>&1 || true)"
if [[ "$public_http_error" != *"plain HTTP is limited to localhost/private addresses"* ]]; then
  echo "installer did not reject a public plain-HTTP URL" >&2
  exit 1
fi

stage_dir="$test_root/explicit-stage"
run_installer --public-url https://monitor.example.test
grep -F 'public_url: "https://monitor.example.test"' "$stage_dir/etc/hostpin/hostpin.yaml" >/dev/null
grep -F 'listen: "0.0.0.0:8080"' "$stage_dir/etc/hostpin/hostpin.yaml" >/dev/null

printf 'server_installer=passed\n'
