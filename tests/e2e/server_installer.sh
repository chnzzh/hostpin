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
  HOSTPIN_RELEASE_BASE="file://$release_dir" \
    sh "$project_root/scripts/install-server.sh" \
      --public-url https://monitor.example.test \
      --listen 127.0.0.1:8080 \
      "$@"
}

write_fixture "first Hostpin server"
run_installer

installed="$stage_dir/usr/local/bin/hostpin-server"
config="$stage_dir/etc/hostpin/hostpin.yaml"
unit="$stage_dir/etc/systemd/system/hostpin-server.service"
cmp "$artifact" "$installed"
grep -F 'public_url: "https://monitor.example.test"' "$config" >/dev/null
grep -F 'listen: "127.0.0.1:8080"' "$config" >/dev/null
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

printf 'server_installer=passed\n'
