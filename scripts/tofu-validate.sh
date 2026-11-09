#!/usr/bin/env bash
# Validates every generated tofu config in internal/tfgen/testdata against
# the real providers: tofu downloads lxc/incus and bpg/proxmox at the pinned
# versions and checks each attribute name and type against their schemas.
# Needs tofu on PATH and network access to the OpenTofu registry; no
# credentials and no host (validate never configures the provider).
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
export TF_PLUGIN_CACHE_DIR="$work/plugins" TF_IN_AUTOMATION=1
mkdir -p "$TF_PLUGIN_CACHE_DIR"

shopt -s nullglob
files=(internal/tfgen/testdata/*.golden.json)
if [ "${#files[@]}" -eq 0 ]; then
  echo "tofu-validate: no golden files found" >&2
  exit 1
fi

failed=0
for f in "${files[@]}"; do
  name=$(basename "$f" .golden.json)
  dir="$work/$name"
  mkdir -p "$dir"
  cp "$f" "$dir/main.tf.json"
  printf '== %s\n' "$name"
  if ! tofu -chdir="$dir" init -backend=false -input=false -no-color >"$dir/init.log" 2>&1; then
    cat "$dir/init.log" >&2
    failed=1
    continue
  fi
  if ! tofu -chdir="$dir" validate -no-color; then
    failed=1
  fi
done

if [ "$failed" -ne 0 ]; then
  echo "tofu-validate: at least one generated config is invalid" >&2
  exit 1
fi
echo "tofu-validate: ${#files[@]} configs valid"
