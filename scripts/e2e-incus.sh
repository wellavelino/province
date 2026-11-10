#!/usr/bin/env bash
# End-to-end test against a real Incus server through its `local` remote.
# Needs: go, tofu and incus on PATH, and an Incus the current user can use.
#
# Without TS_AUTHKEY it stops after the plan, which already runs the real
# preflight (`incus query`) and a real `tofu plan` against the server.
# With TS_AUTHKEY (a tagged Tailscale auth key; TS_TAG names its tag,
# default tag:dev) it runs the full lifecycle: create, bootstrap, recipe,
# show, provision, destroy. Set TS_API_KEY too so destroy removes the node.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

work=$(mktemp -d)
name="ci-${GITHUB_RUN_ID:-$$}"
province="$work/province"
created=0

cleanup() {
  if [ "$created" = 1 ]; then
    "$province" env destroy "$name" --yes || incus delete --force "local:$name" || true
  fi
  rm -rf "$work"
}
trap cleanup EXIT

step() { printf '\n== %s\n' "$*"; }

step "build"
go build -o "$province" ./cmd/province
ssh-keygen -q -t ed25519 -N '' -f "$work/id"

cat >"$work/province.yaml" <<YAML
state_dir: $work/state
user: dev
ssh_public_keys: [$work/id.pub]
tailscale: { tag: "${TS_TAG:-tag:dev}" }
defaults: { cpu: 1, ram: 1024, disk: 4, recipe: test }
hosts:
  - { name: local, backend: incus, remote: local }
recipes:
  test:
    packages: [jq]
    run: ['echo "\$P_USER@\$P_HOSTNAME" > /root/recipe-ran']
YAML
export PROVINCE_CONFIG="$work/province.yaml"

step "plan (real incus preflight, real tofu plan)"
"$province" env create "$name"
"$province" env list

if [ -z "${TS_AUTHKEY:-}" ]; then
  step "TS_AUTHKEY is not set: stopping after the plan"
  exit 0
fi

step "create, bootstrap, recipe"
created=1
"$province" env create "$name" --apply

step "check the guest"
incus exec "local:$name" -- tailscale ip -4
incus exec "local:$name" -- dpkg-query -W jq
got=$(incus exec "local:$name" -- cat /root/recipe-ran)
if [ "$got" != "dev@$name" ]; then
  echo "recipe run wrote '$got', want 'dev@$name'" >&2
  exit 1
fi
"$province" env show "$name" --json | tee "$work/show.json"
grep -q '"live_status": *"running"' "$work/show.json"

step "provision again"
"$province" env provision "$name"

step "destroy"
"$province" env destroy "$name" --yes
created=0
if incus info "local:$name" >/dev/null 2>&1; then
  echo "instance $name still exists after destroy" >&2
  exit 1
fi
echo "e2e-incus: all steps passed"
