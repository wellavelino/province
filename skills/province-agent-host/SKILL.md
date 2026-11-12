---
name: province-agent-host
description: Set up a long-lived agent host for province — an Incus server inside a Proxmox VM, a restricted Incus project for task envs, and a long-lived Claude Code env reachable from a phone. Use when the owner asks to build, check or repair that setup. One-time, owner-gated work; for running tasks once it exists, use the province-task-envs skill.
---

# province agent host

The layout this skill builds:

```
Proxmox host
└── Incus VM (CPU type host, nested KVM)          ← the Incus server
    ├── agent-host env (long-lived)               ← Claude Code in tmux
    └── task envs (short-lived, project "tasks")  ← created and destroyed by the agent
```

Task envs sit **next to** the agent-host env, never inside it: the agent asks the Incus server for them over its API. Never nest a third virtualization level.

## Rules

1. Every step here changes real hosts. Run a step only when the owner asked for it in this conversation; show the commands first and wait for a yes. One Proxmox host may carry production.
2. Never print, echo or log a token, auth key or trust token. Pass them through the environment, and tell the owner where to paste them rather than pasting them yourself.
3. Stop and ask when a check fails. Do not work around a missing `/dev/kvm`, a full disk or a refused permission.

## 1. Proxmox: the Incus VM

Check nested KVM on the Proxmox host (prints `1` or `Y`):

```
cat /sys/module/kvm_amd/parameters/nested     # kvm_intel on Intel hosts
```

Create a Debian 13 VM for Incus with CPU type `host` (without it there is no `/dev/kvm` inside), at least 4 cores, 8 GiB RAM and enough disk for every task env it will hold. Start it on boot.

```
qm set <vmid> --cpu host --onboot 1
```

## 2. Inside the Incus VM: Incus and the project

```
apt install incus
incus admin init                      # defaults are fine
ls /dev/kvm                           # must exist, or VMs will not start
incus config set core.https_address :8443
```

Create the restricted project task envs live in. The limits are the safety net when an agent loops:

```
incus project create tasks \
  -c features.images=false -c features.profiles=false \
  -c restricted=true \
  -c limits.instances=6 -c limits.cpu=12 -c limits.memory=24GiB -c limits.disk=200GiB
```

Ask the owner for the numbers; the ones above are a starting point. `restricted=true` also blocks unix-char device passthrough, so keep the site config's `tailscale.tun` at its default, `userspace`; `device` fails in this project (VMs need neither).

Smoke test:

```
incus launch images:debian/13 smoke --vm --project tasks
incus exec smoke --project tasks -- uname -a
incus delete smoke --force --project tasks
```

## 3. The agent-host env

Created from the owner's laptop with province, in the default project (not `tasks`), so task limits never squeeze it:

```
province env create agent-host --host <incus-host> --recipe <agent-recipe> --json
province env create agent-host --host <incus-host> --recipe <agent-recipe> --apply --json
```

The recipe should install `tmux`, `git`, `incus` (the client), `tofu`, `province` and Claude Code. Use `--protect` when province supports it, so the env cannot be destroyed by mistake.

## 4. Give the agent-host access to the tasks project only

On the Incus server, a trust token restricted to the `tasks` project:

```
incus config trust add agent-host --projects tasks --restricted
```

In the agent-host env (the owner pastes the token):

```
incus remote add tasks-box https://<incus-vm-magicdns>:8443 --token <token>
incus list tasks-box: --project tasks
```

Its site config names that remote and project:

```yaml
hosts:
  - { name: tasks, backend: incus, remote: tasks-box, project: tasks }
```

For `TS_AUTHKEY`, the agent-host gets its own Tailscale auth key: reusable, ephemeral, pre-authorized, tagged for task envs only (for example `tag:task`), so the tailnet policy decides what task envs can reach. Never give it the owner's main key or `TS_API_KEY` with wide scope.

## 5. Long-lived session

```
ssh dev@agent-host
tmux new -s agent        # first time; later: tmux attach -t agent
claude
```

From a phone: any SSH app on the tailnet, then `tmux attach -t agent`.

## Check the setup

- `incus list tasks-box: --project tasks` works from the agent-host env.
- `incus list tasks-box:` without the project is refused (the token is restricted).
- A task env created from the agent-host appears in the `tasks` project and on the tailnet with the task tag.
- `incus project info tasks` shows the limits.
