#!/usr/bin/env bash
# province recipe. Runs as root inside a bootstrapped guest through the
# backend's exec (`pct exec` or `incus exec`) with `bash -s`; province
# prepends shell-quoted P_* assignments on stdin. Every step is safe to
# re-run, so env provision applies the same recipe again.
# Inputs (newline-separated lists, any may be empty except P_USER):
#   P_USER      the env's sudo user
#   P_HOSTNAME  the env's name
#   P_PACKAGES  apt package names
#   P_TOOLS     mise tools as name@version
#   P_DOTFILES  https git URL cloned into ~/.dotfiles
#   P_RUN_N     number of run commands, each in P_RUN_0, P_RUN_1, ...
export LC_ALL=C.UTF-8 LANG=C.UTF-8
set -euo pipefail
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
export HOME=/root
export DEBIAN_FRONTEND=noninteractive
MISE=/usr/local/bin/mise
# shellcheck disable=SC2016 # expanded by the user's shell, not here
SHIMS_LINE='export PATH="$HOME/.local/share/mise/shims:$PATH" # province: mise tools'

log() { printf 'province-recipe: %s\n' "$*" >&2; }

check_inputs() {
  : "${P_USER:?}" "${P_HOSTNAME:?}" "${P_RUN_N:?}"
  : "${P_PACKAGES?}" "${P_TOOLS?}" "${P_DOTFILES?}" # may be empty
  if ! id -u "$P_USER" >/dev/null 2>&1; then
    log "user $P_USER does not exist: is the env bootstrapped?"
    return 1
  fi
  USER_HOME=$(getent passwd "$P_USER" | cut -d: -f6)
}

# as_user runs a command as the env's user, in their home, with a login
# environment, so tools land in ~ and not in /root.
as_user() {
  sudo -u "$P_USER" -H env -C "$USER_HOME" "PATH=$USER_HOME/.local/share/mise/shims:$USER_HOME/.local/bin:$PATH" "$@"
}

# apt_install installs packages (one per argument) that are not installed yet.
apt_install() {
  local -a missing=()
  local p
  for p in "$@"; do
    if ! dpkg-query -W -f='${Status}' "${p%%=*}" 2>/dev/null | grep -q 'install ok installed'; then
      missing+=("$p")
    fi
  done
  if [ "${#missing[@]}" -eq 0 ]; then
    return 0
  fi
  log "installing packages: ${missing[*]}"
  apt-get update -q
  apt-get install -y -q "${missing[@]}"
}

install_packages() {
  local -a pkgs=()
  if [ -z "$P_PACKAGES" ]; then
    return 0
  fi
  mapfile -t pkgs <<<"$P_PACKAGES"
  apt_install "${pkgs[@]}"
}

install_tools() {
  local -a tools=()
  local rc="$USER_HOME/.bashrc"
  if [ -z "$P_TOOLS" ]; then
    return 0
  fi
  mapfile -t tools <<<"$P_TOOLS"
  if [ ! -x "$MISE" ]; then
    apt_install curl ca-certificates
    log "installing mise"
    curl -fsSL https://mise.run | MISE_INSTALL_PATH="$MISE" sh
  fi
  # The shims go first in .bashrc, before Debian's "not interactive" early
  # return, so `ssh dev@env 'go test ./...'` finds the tools too.
  touch "$rc"
  chown "$P_USER:$P_USER" "$rc"
  if ! grep -qF "$SHIMS_LINE" "$rc"; then
    printf '%s\n%s\n' "$SHIMS_LINE" "$(cat "$rc")" >"$rc.province"
    chown "$P_USER:$P_USER" "$rc.province"
    mv -f "$rc.province" "$rc"
  fi
  log "installing tools: ${tools[*]}"
  as_user env MISE_YES=1 "$MISE" use --global "${tools[@]}"
  as_user "$MISE" reshim
}

install_dotfiles() {
  local dir="$USER_HOME/.dotfiles"
  if [ -z "$P_DOTFILES" ]; then
    return 0
  fi
  apt_install git ca-certificates
  if [ -d "$dir/.git" ]; then
    log "updating dotfiles in $dir"
    as_user git -C "$dir" pull --ff-only
  else
    log "cloning dotfiles into $dir"
    as_user git clone "$P_DOTFILES" "$dir"
  fi
  if [ -f "$dir/install.sh" ]; then
    log "running dotfiles install.sh"
    as_user env -C "$dir" bash ./install.sh
  fi
}

run_commands() {
  local i var
  export P_USER P_HOSTNAME
  for ((i = 0; i < P_RUN_N; i++)); do
    var="P_RUN_$i"
    log "run[$i]: ${!var}"
    bash -euo pipefail -c "${!var}"
  done
}

main() {
  exec 1>&2
  check_inputs
  install_packages
  install_tools
  install_dotfiles
  run_commands
  log "done"
}

main "$@" </dev/null
