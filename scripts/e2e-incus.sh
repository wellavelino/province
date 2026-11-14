#!/usr/bin/env bash
# End-to-end test against a real Incus server through its `local` remote,
# run on the Incus host itself. Needs: go, tofu, incus, jq, curl and
# python3 on PATH, and an Incus the current user can use; for the isolated
# env, Incus 6.0.4+ (6.4+ outside the LTS) with the nftables firewall.
#
# Two envs, to keep creates few:
#   C: a container on an isolated host (network incusbr0), --protect --ttl 1s
#   V: a VM (--kind vm) on a plain host, --ttl 1s; a container if the
#      server cannot run VMs (no /dev/kvm or no qemu driver)
#
# Without TS_AUTHKEY it does what needs no tailnet: the plans for both
# (real incus preflight, including the isolated checks, and a real tofu
# plan), env list/show/gc on the planned envs, and the --protect refusal.
# With TS_AUTHKEY (a tagged Tailscale auth key; TS_TAG names its tag,
# default tag:dev) it also runs the full lifecycle of both (create,
# bootstrap, recipe, show, provision, destroy), the isolation probes from
# inside C (V is the control: same bridge, no ACL), and --ttl with env gc.
# Set TS_API_KEY too so destroy removes the tailnet nodes.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

work=$(mktemp -d)
base="ci-${GITHUB_RUN_ID:-$$}-${GITHUB_RUN_ATTEMPT:-0}"
c="$base-c" # isolated ACL names are province-<env>: keep under 55 chars
v="$base-v"
province="$work/province"
envs=()     # every env this run planned or created, for cleanup
listener="" # pid of the LAN stand-in HTTP server
port=8099

cleanup() {
  set +e
  [ -n "$listener" ] && kill "$listener" 2>/dev/null
  for n in "${envs[@]}"; do
    if [ -d "$work/state/$n" ]; then
      "$province" env protect "$n" --off >/dev/null
      "$province" env destroy "$n" --yes
    fi
    if incus info "local:$n" >/dev/null 2>&1; then
      echo "cleanup: $n still exists, deleting it with incus" >&2
      incus delete --force "local:$n"
    fi
    if incus network acl show "local:province-$n" >/dev/null 2>&1; then
      echo "cleanup: ACL province-$n still exists, deleting it" >&2
      incus network acl delete "local:province-$n"
    fi
  done
  rm -rf "$work"
}
trap cleanup EXIT

step() { printf '\n== %s\n' "$*"; }
skip() { printf 'skipped: %s\n' "$*"; }
fail() {
  echo "e2e-incus: FAIL: $*" >&2
  exit 1
}

# run WANT CMD...: run CMD with its stdout in $out, fail unless it exits WANT.
out="$work/out.json"
run() {
  local want=$1 rc=0
  shift
  "$@" >"$out" || rc=$?
  cat "$out"
  [ "$rc" = "$want" ] || fail "$* exited $rc, want $want"
}

# check FILTER WHAT: fail unless jq FILTER is true on $out.
check() { jq -e "$1" "$out" >/dev/null || fail "$2 (jq: $1)"; }

exists() { incus info "local:$1" >/dev/null 2>&1; }

# probe ENV URL: exit code of curl in ENV (0 means reached).
probe() {
  local rc=0
  timeout 30 incus exec "local:$1" -- curl -sS --connect-timeout 5 --max-time 10 -o /dev/null "$2" || rc=$?
  echo "$rc"
}

step "build"
go build -o "$province" ./cmd/province
ssh-keygen -q -t ed25519 -N '' -f "$work/id"

cat >"$work/province.yaml" <<YAML
state_dir: $work/state
user: dev
ssh_public_keys: [$work/id.pub]
tailscale: { tag: "${TS_TAG:-tag:dev}", tun: userspace }
defaults: { cpu: 1, ram: 1024, disk: 4, recipe: test }
hosts:
  - { name: local, backend: incus, remote: local }
  - { name: iso, backend: incus, remote: local, network: incusbr0, isolated: true }
recipes:
  test:
    packages: [jq, curl, ca-certificates]
    run: ['echo "\$P_USER@\$P_HOSTNAME" > /root/recipe-ran']
YAML
export PROVINCE_CONFIG="$work/province.yaml"

# VMs need KVM in the Incus daemon (root); the runner user needs no access.
vkind=lxc
if [ ! -e /dev/kvm ]; then
  skip "VM: /dev/kvm is absent on this machine; V is a container instead"
elif ! incus query /1.0 | jq -e '.environment.driver | test("qemu")' >/dev/null; then
  skip "VM: Incus reports no qemu driver ($(incus query /1.0 | jq -r .environment.driver)); V is a container instead"
else
  vkind=vm
fi

step "plan C: isolated container, --protect --ttl 1s (real incus preflight, real tofu plan)"
envs+=("$c")
run 0 "$province" env create "$c" --host iso --protect --ttl 1s --json
check '.status == "planned" and .kind == "lxc" and .backend == "incus"' "C plan"

step "plan V: --kind vm, --ttl 1s"
envs+=("$v")
run 0 "$province" env create "$v" --host local --kind vm --ttl 1s --json
check '.status == "planned" and .kind == "vm"' "V plan"
if [ "$vkind" = lxc ]; then
  # The kind is recorded with the env: drop the VM plan, plan a container.
  run 0 "$province" env destroy "$v" --yes --json
  run 0 "$province" env create "$v" --host local --kind lxc --ttl 1s --json
fi

step "list and show the planned envs"
"$province" env list
run 0 "$province" env list --json
check '.envs | length == 2' "list has both envs"
check ".envs[] | select(.name == \"$c\") | .isolated and .protected and .ttl == \"1s\" and .live_status == \"not_created\" and (.expired | not)" "C in list"
check ".envs[] | select(.name == \"$v\") | .kind == \"$vkind\" and .ttl == \"1s\" and (.expires_at == null) and (.expired | not)" "V in list"
run 0 "$province" env show "$c" --json
check '.isolated and .protected and .status == "planned"' "C show"

step "env gc with nothing expired: exit 0, nothing listed"
run 0 "$province" env gc --json
check '.envs == [] and .failed == 0' "gc on planned envs"

if [ -n "${TS_AUTHKEY:-}" ]; then
  step "create C (isolated container): bootstrap, recipe"
  run 0 "$province" env create "$c" --apply --json
  step "create V ($vkind): bootstrap, recipe"
  run 0 "$province" env create "$v" --apply --json
  check ".kind == \"$vkind\"" "V kind after apply"

  for n in "$c" "$v"; do
    step "check the guest $n"
    incus exec "local:$n" -- tailscale ip -4
    incus exec "local:$n" -- dpkg-query -W jq curl
    got=$(incus exec "local:$n" -- cat /root/recipe-ran)
    [ "$got" = "dev@$n" ] || fail "recipe run in $n wrote '$got', want 'dev@$n'"
    run 0 "$province" env show "$n" --json
    check '.live_status == "running" and .status == "bootstrapped"' "$n show"
  done
  [ "$(incus query "/1.0/instances/$v" | jq -r .type)" = "$([ "$vkind" = vm ] && echo virtual-machine || echo container)" ] ||
    fail "V is not a $vkind on the server"
  incus network acl show "local:province-$c" >/dev/null || fail "ACL province-$c is missing"
  run 0 "$province" env show "$c" --json
  check '.isolated' "C show isolated"

  step "isolation: C reaches the internet and DNS, not the LAN; V (no ACL) is the control"
  # A stand-in LAN service on the Incus host, on every address: the
  # runner's primary IPv4 and the bridge gateway, on a non-DNS port.
  mkdir "$work/www" && echo ok >"$work/www/index.html"
  python3 -m http.server "$port" --bind 0.0.0.0 --directory "$work/www" >/dev/null 2>&1 &
  listener=$!
  host_ip=$(ip -4 route get 1.1.1.1 | awk '{for (i = 1; i < NF; i++) if ($i == "src") { print $(i + 1); exit }}')
  gw=$(incus network get local:incusbr0 ipv4.address)
  gw=${gw%/*}
  echo "runner primary IPv4: $host_ip, bridge gateway: $gw"
  targets=("$gw")
  case "$host_ip" in
  10.* | 192.168.* | 172.1[6-9].* | 172.2[0-9].* | 172.3[01].*) targets+=("$host_ip") ;;
  *) skip "probe of $host_ip: not a private address, so not LAN to the ACL" ;;
  esac
  for i in 1 2 3 4 5 6 7 8 9 10; do
    curl -fsS --max-time 2 -o /dev/null "http://$gw:$port/" && break
    [ "$i" = 10 ] && fail "the stand-in LAN service on port $port did not start"
    sleep 1
  done
  for ip in "${targets[@]}"; do
    rc=$(probe "$v" "http://$ip:$port/")
    [ "$rc" = 0 ] || fail "control: V could not reach $ip:$port (curl exit $rc), so the probe proves nothing"
    rc=$(probe "$c" "http://$ip:$port/")
    case "$rc" in
    7 | 28) echo "ok: C cannot reach $ip:$port (curl exit $rc)" ;;
    0) fail "isolated C reached $ip:$port on the LAN" ;;
    *) fail "probe of $ip:$port from C failed oddly (exit $rc): not a network refusal" ;;
    esac
  done
  incus exec "local:$c" -- getent ahostsv4 www.debian.org || fail "DNS does not work in C"
  rc=$(probe "$c" "https://www.debian.org/")
  [ "$rc" = 0 ] || fail "C cannot reach https://www.debian.org (curl exit $rc)"
  echo "ok: C resolves names and reaches the internet"

  step "provision C again"
  run 0 "$province" env provision "$c" --json

  step "--ttl: both envs have expired (1s), env list says so"
  run 0 "$province" env list --json
  check '[.envs[] | select(.expired and .expires_at != null)] | length == 2' "both envs expired"

  step "env gc without --yes: exit 2, names V, changes nothing"
  run 2 "$province" env gc --json
  check ".code == \"refused\" and (.error | contains(\"$v\")) and (.error | contains(\"skipping protected: $c\"))" "gc dry run"
  exists "$v" || fail "env gc without --yes removed $v"

  step "env gc --yes: destroys V, skips protected C"
  run 0 "$province" env gc --yes --json
  check ".failed == 0 and ([.envs[] | select(.name == \"$v\" and .outcome == \"destroyed\")] | length == 1) and ([.envs[] | select(.name == \"$c\" and .outcome == \"skipped\" and .reason == \"protected\")] | length == 1)" "gc --yes"
  if exists "$v"; then fail "$v still exists after env gc --yes"; fi
  exists "$c" || fail "env gc --yes removed protected $c"
else
  skip "full lifecycle of C and V, isolation probes, --ttl and env gc --yes: no TS_AUTHKEY"
fi

step "--protect: destroy is refused (exit 2) and changes nothing"
run 2 "$province" env destroy "$c" --yes --json
check '.code == "refused" and (.error | contains("protected"))' "protected destroy"
[ -d "$work/state/$c" ] || fail "refused destroy removed $c's state"
if [ -n "${TS_AUTHKEY:-}" ]; then exists "$c" || fail "refused destroy removed $c's guest"; fi

step "env protect --off, then destroy C"
run 0 "$province" env protect "$c" --off --json
check '.protected == false' "protect --off"
run 0 "$province" env destroy "$c" --yes --json
check '.destroyed' "C destroy"
if exists "$c"; then fail "instance $c still exists after destroy"; fi
if incus network acl show "local:province-$c" >/dev/null 2>&1; then fail "ACL province-$c still exists after destroy"; fi

if [ -d "$work/state/$v" ]; then
  step "destroy V (planned only)"
  run 0 "$province" env destroy "$v" --yes --json
fi
run 0 "$province" env list --json
check '.envs == []' "no envs left"
echo "e2e-incus: all steps passed"
