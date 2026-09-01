#!/usr/bin/env bash
set -euo pipefail

project_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$project_root"

GO_BIN="${GO:-go}"
OUTPUT_DIR="${1:?usage: collect-go-licenses.sh OUTPUT_DIR}"

mkdir -p "$OUTPUT_DIR"
if [[ -n "$(find "$OUTPUT_DIR" -mindepth 1 -maxdepth 1 -print -quit)" ]]; then
  echo "license output directory must be empty: $OUTPUT_DIR" >&2
  exit 1
fi

module_list="$(mktemp "${TMPDIR:-/tmp}/hostpin-go-modules.XXXXXX")"
trap 'rm -f "$module_list"' EXIT

"$GO_BIN" list -m -f '{{if .Dir}}{{.Path}}{{"\t"}}{{.Version}}{{"\t"}}{{.Dir}}{{end}}' all > "$module_list"

mkdir -p "$OUTPUT_DIR/modules" "$OUTPUT_DIR/toolchain"
printf '# Go module licenses included in Hostpin release binaries\n\n' > "$OUTPUT_DIR/MODULES.txt"

module_count=0
while IFS=$'\t' read -r module_path module_version module_dir; do
  if [[ -z "$module_version" || -z "$module_dir" ]]; then
    continue
  fi
  if [[ "$module_path" == *".."* || "$module_version" == *"/"* ]]; then
    echo "unsafe module path reported by Go: $module_path $module_version" >&2
    exit 1
  fi

  destination="$OUTPUT_DIR/modules/${module_path}@${module_version}"
  found=0
  while IFS= read -r -d '' license_file; do
    relative_path="${license_file#"$module_dir"/}"
    install_path="$destination/$relative_path"
    mkdir -p "$(dirname "$install_path")"
    install -m 0644 "$license_file" "$install_path"
    found=1
  done < <(
    find "$module_dir" -maxdepth 2 -type f \( \
      -iname 'LICENSE' -o -iname 'LICENSE.*' -o -iname 'LICENSE-*' -o \
      -iname 'COPYING' -o -iname 'COPYING.*' -o -iname 'COPYING-*' -o \
      -iname 'NOTICE' -o -iname 'NOTICE.*' -o -iname 'NOTICE-*' -o \
      -iname 'COPYRIGHT' -o -iname 'COPYRIGHT.*' -o -iname 'COPYRIGHT-*' -o \
      -iname 'PATENTS' -o -iname 'PATENTS.*' -o -iname 'PATENTS-*' \
    \) -print0
  )
  if [[ "$found" -ne 1 ]]; then
    echo "no license file found for Go module: $module_path $module_version" >&2
    exit 1
  fi
  printf '%s %s\n' "$module_path" "$module_version" >> "$OUTPUT_DIR/MODULES.txt"
  module_count=$((module_count + 1))
done < "$module_list"

go_root="$("$GO_BIN" env GOROOT)"
install -m 0644 "$go_root/LICENSE" "$OUTPUT_DIR/toolchain/LICENSE"
if [[ -f "$go_root/PATENTS" ]]; then
  install -m 0644 "$go_root/PATENTS" "$OUTPUT_DIR/toolchain/PATENTS"
fi
"$GO_BIN" version > "$OUTPUT_DIR/toolchain/VERSION.txt"

printf 'Collected license texts for %d Go modules.\n' "$module_count"
