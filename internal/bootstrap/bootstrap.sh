#!/usr/bin/env bash
# province bootstrap. Runs as root inside a new guest (container or VM) through
# `pct exec <vmid> -- bash -s` or `incus exec`; province prepends shell-quoted P_* and
# TS_AUTHKEY assignments on stdin. Safe to re-run. TS_AUTHKEY is empty when
# province found the guest already on the tailnet: a joined node never gets
# the key again. The key is unset right after `tailscale up`, before Docker
# or anything else is installed.
# Everything goes to stderr except the final PROVINCE_TS_IP=<ip> line.
# The operator's ssh may forward a locale the fresh guest lacks; use one
# that always exists so no tool warns about setlocale.
export LC_ALL=C.UTF-8 LANG=C.UTF-8
set -euo pipefail
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
export HOME=/root
export DEBIAN_FRONTEND=noninteractive
KEYFILE=/run/province.authkey
LOCALE_GEN=/etc/locale.gen

log() { printf 'province-bootstrap: %s\n' "$*" >&2; }

check_inputs() {
  : "${P_USER:?}" "${P_KEYS:?}" "${P_HOSTNAME:?}" "${P_TAG:?}" "${P_TUN:?}" "${P_DOCKER:?}" "${P_TS_SSH:?}"
  : "${P_LOCALES?}" # may be empty
  : "${TS_AUTHKEY?}" # empty when the guest is already on the tailnet
}

wait_network() {
  local tries=0
  while [ "$tries" -lt 120 ]; do
    if getent hosts deb.debian.org >/dev/null 2>&1; then
      return 0
    fi
    tries=$((tries + 1))
    sleep 1
  done
  log "no DNS after 120 s (getent hosts deb.debian.org failed)"
  return 1
}

install_base() {
  log "installing base packages"
  apt-get update -q
  apt-get install -y -q curl ca-certificates sudo openssh-server
  if ! systemctl is-active --quiet ssh && ! systemctl is-active --quiet ssh.socket; then
    systemctl enable --now ssh
  fi
}

# setup_locales generates the locales operators' ssh clients forward
# (P_LOCALES, one per line), so interactive logins have them. Idempotent.
setup_locales() {
  local loc need=0
  if [ -z "$P_LOCALES" ]; then
    return 0
  fi
  apt-get install -y -q locales
  touch "$LOCALE_GEN"
  while IFS= read -r loc; do
    if [ -z "$loc" ]; then
      continue
    fi
    if ! grep -qx "$loc UTF-8" "$LOCALE_GEN"; then
      if grep -qx "# *$loc UTF-8" "$LOCALE_GEN"; then
        sed -i "s|^# *$loc UTF-8\$|$loc UTF-8|" "$LOCALE_GEN"
      else
        printf '%s UTF-8\n' "$loc" >>"$LOCALE_GEN"
      fi
    fi
    # locale -a lists en_US.UTF-8 as en_US.utf8.
    if ! locale -a 2>/dev/null | grep -qix "${loc%%.*}.utf8"; then
      need=1
    fi
  done <<<"$P_LOCALES"
  if [ "$need" = 1 ]; then
    log "generating locales"
    locale-gen
  fi
}

setup_user() {
  local home sudoers
  if ! id -u "$P_USER" >/dev/null 2>&1; then
    log "creating user $P_USER"
    useradd -m -s /bin/bash "$P_USER"
  fi
  sudoers="/etc/sudoers.d/90-province-$P_USER"
  printf '%s ALL=(ALL) NOPASSWD:ALL\n' "$P_USER" >"$sudoers.tmp"
  chmod 0440 "$sudoers.tmp"
  visudo -cf "$sudoers.tmp" >/dev/null
  mv -f "$sudoers.tmp" "$sudoers"
  home=$(getent passwd "$P_USER" | cut -d: -f6)
  install -d -m 0700 -o "$P_USER" -g "$P_USER" "$home/.ssh"
  printf '%s\n' "$P_KEYS" >"$home/.ssh/authorized_keys"
  chown "$P_USER:$P_USER" "$home/.ssh/authorized_keys"
  chmod 0600 "$home/.ssh/authorized_keys"
}

install_docker() {
  if [ "$P_DOCKER" != 1 ]; then
    return 0
  fi
  if ! command -v docker >/dev/null 2>&1; then
    log "installing Docker"
    curl -fsSL https://get.docker.com | sh
  fi
  usermod -aG docker "$P_USER"
}

install_tailscale() {
  if ! command -v tailscale >/dev/null 2>&1; then
    log "installing Tailscale"
    curl -fsSL https://tailscale.com/install.sh | sh
  fi
  if [ "$P_TUN" = userspace ]; then
    if grep -q '^FLAGS=' /etc/default/tailscaled 2>/dev/null; then
      sed -i 's|^FLAGS=.*|FLAGS="--tun=userspace-networking"|' /etc/default/tailscaled
    else
      printf 'FLAGS="--tun=userspace-networking"\n' >>/etc/default/tailscaled
    fi
  elif [ ! -c /dev/net/tun ]; then
    log "tailscale.tun is device but /dev/net/tun is missing in the container"
    return 1
  fi
  systemctl enable tailscaled
  systemctl restart tailscaled
}

join_tailnet() {
  local ssh=false
  local -a up_flags=()
  if [ "$P_TS_SSH" = 1 ]; then
    ssh=true
    up_flags+=(--ssh)
  fi
  if tailscale ip -4 >/dev/null 2>&1; then
    log "already on the tailnet; setting Tailscale SSH to $ssh"
    # Bootstrap runs over `pct exec`, not the guest's SSH, so toggling
    # Tailscale SSH cannot cut this session.
    tailscale set --ssh="$ssh" --accept-risk=lose-ssh
    unset TS_AUTHKEY
    return 0
  fi
  if [ -z "$TS_AUTHKEY" ]; then
    log "not on the tailnet and province sent no auth key (it saw the guest joined); re-run the bootstrap"
    return 1
  fi
  trap 'rm -f "$KEYFILE"' EXIT
  (umask 077 && printf '%s' "$TS_AUTHKEY" >"$KEYFILE")
  log "joining the tailnet as $P_HOSTNAME (Tailscale SSH: $ssh)"
  tailscale up --auth-key "file:$KEYFILE" --hostname "$P_HOSTNAME" --advertise-tags "$P_TAG" --timeout 120s --accept-risk=lose-ssh "${up_flags[@]}"
  rm -f "$KEYFILE"
  unset TS_AUTHKEY
}

report() {
  local ip
  ip=$(tailscale ip -4 | head -n 1)
  printf 'PROVINCE_TS_IP=%s\n' "$ip" >&3
}

main() {
  exec 3>&1 1>&2
  check_inputs
  wait_network
  install_base
  setup_locales
  setup_user
  install_tailscale
  # Join, and drop the key, before Docker's installer or any later step runs.
  join_tailnet
  install_docker
  report
}

main "$@" </dev/null
