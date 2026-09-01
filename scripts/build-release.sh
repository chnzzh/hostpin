#!/usr/bin/env bash
set -euo pipefail

project_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$project_root"

: "${VERSION:?set VERSION to a tag such as v0.1.0}"
: "${UPDATE_PUBLIC_KEY:?set UPDATE_PUBLIC_KEY to the base64 Ed25519 public key}"

GO_BIN="${GO:-go}"
OUTPUT_DIR="${OUTPUT_DIR:-dist}"
COMMIT="${COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || printf unknown)}"
RELEASE_BASE="${RELEASE_BASE:-https://github.com/chnzzh/hostpin/releases/latest/download}"

mkdir -p "$OUTPUT_DIR"
if [[ -n "$(find "$OUTPUT_DIR" -mindepth 1 -maxdepth 1 -print -quit)" ]]; then
  echo "release output directory must be empty: $OUTPUT_DIR" >&2
  exit 1
fi

ldflags="-s -w -X github.com/chnzzh/hostpin/internal/buildinfo.Version=${VERSION} -X github.com/chnzzh/hostpin/internal/buildinfo.Commit=${COMMIT} -X github.com/chnzzh/hostpin/internal/buildinfo.ReleaseBase=${RELEASE_BASE}"
agent_ldflags="${ldflags} -X github.com/chnzzh/hostpin/internal/updater.PublicKey=${UPDATE_PUBLIC_KEY}"
agent_targets=(
  linux/amd64 linux/arm64 linux/386 linux/arm/7 linux/mips linux/mipsle linux/riscv64
  windows/amd64 windows/arm64 darwin/amd64 darwin/arm64 freebsd/amd64 freebsd/arm64
)
artifacts=()

for target in "${agent_targets[@]}"; do
  IFS=/ read -r target_os target_arch target_arm <<< "$target"
  release_arch="$target_arch"
  if [[ "$target_arch" == "arm" && "$target_arm" == "7" ]]; then
    release_arch=armv7
  fi
  suffix=""
  if [[ "$target_os" == "windows" ]]; then
    suffix=.exe
  fi
  output="$OUTPUT_DIR/hostpin-agent-${target_os}-${release_arch}${suffix}"
  if [[ "$target_arch" == "mips" || "$target_arch" == "mipsle" ]]; then
    GOOS="$target_os" GOARCH="$target_arch" GOMIPS=softfloat CGO_ENABLED=0 \
      "$GO_BIN" build -trimpath -ldflags="$agent_ldflags" -o "$output" ./cmd/hostpin-agent
  else
    GOOS="$target_os" GOARCH="$target_arch" GOARM="${target_arm:-}" CGO_ENABLED=0 \
      "$GO_BIN" build -trimpath -ldflags="$agent_ldflags" -o "$output" ./cmd/hostpin-agent
  fi
  artifacts+=("$output")
done

for target_arch in amd64 arm64; do
  output="$OUTPUT_DIR/hostpin-server-linux-${target_arch}"
  GOOS=linux GOARCH="$target_arch" CGO_ENABLED=0 \
    "$GO_BIN" build -trimpath -ldflags="$ldflags" -o "$output" ./cmd/hostpin-server
  artifacts+=("$output")
done

install -m 0644 LICENSE "$OUTPUT_DIR/LICENSE"
install -m 0644 THIRD_PARTY_NOTICES.md "$OUTPUT_DIR/THIRD_PARTY_NOTICES.md"
install -m 0755 scripts/install-server.sh "$OUTPUT_DIR/install-server.sh"
license_stage="$(mktemp -d "${TMPDIR:-/tmp}/hostpin-release-licenses.XXXXXX")"
trap 'rm -rf "$license_stage"' EXIT
mkdir -p "$license_stage/static"
cp -R third_party/. "$license_stage/static/"
GO="$GO_BIN" scripts/collect-go-licenses.sh "$license_stage/go"
tar -czf "$OUTPUT_DIR/hostpin-third-party-licenses.tar.gz" -C "$license_stage" .
release_documents=(
  "$OUTPUT_DIR/LICENSE"
  "$OUTPUT_DIR/THIRD_PARTY_NOTICES.md"
  "$OUTPUT_DIR/install-server.sh"
  "$OUTPUT_DIR/hostpin-third-party-licenses.tar.gz"
)

hash_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

for artifact in "${artifacts[@]}"; do
  digest="$(hash_file "$artifact")"
  printf '%s\n' "$digest" > "$artifact.sha256"
done

: > "$OUTPUT_DIR/SHA256SUMS"
for artifact in "${artifacts[@]}" "${release_documents[@]}"; do
  digest="$(hash_file "$artifact")"
  printf '%s  %s\n' "$digest" "$(basename "$artifact")" >> "$OUTPUT_DIR/SHA256SUMS"
done

printf 'Built %d Hostpin release binaries in %s\n' "${#artifacts[@]}" "$OUTPUT_DIR"
