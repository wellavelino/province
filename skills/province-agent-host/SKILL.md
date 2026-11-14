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

Create a Debian 13 VM for Incus with CPU type `host` (without it there is no `/dev/kvm` inside), at least 4 cores, 16 GiB RAM and enough disk for every task env it will hold. Start it on boot. Its RAM must exceed the tasks project's `limits.memory` (section 2) plus the agent-host env and Incus's own overhead: CPU can be shared between envs, memory cannot.

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

Join the Incus VM to the tailnet, so the agent-host env can reach its API by MagicDNS name. The owner runs this in their own terminal on the VM; it prints a login URL, and no key is typed or printed. Tag it (for example `tag:incus`; the tag needs an owner in the tailnet policy) and grant only the agent-host's tag access to its port 8443. Keep the VM's own tailnet grants narrow too: an isolated env's packets to tailnet addresses can be routed into the VM's tailnet connection (see the Tailscale note under "Isolated envs" in docs/incus.md).

```
curl -fsSL https://tailscale.com/install.sh | sh
tailscale up --advertise-tags tag:incus
tailscale status                      # note the VM's MagicDNS name: <incus-vm-magicdns>
```

Create the restricted project task envs live in. The limits are the safety net when an agent loops:

```
incus project create tasks \
  -c features.images=false -c features.profiles=false \
  -c restricted=true \
  -c limits.instances=4 -c limits.cpu=8 -c limits.memory=8GiB -c limits.disk=200GiB
```

Ask the owner for the numbers; the ones above are a starting point for a 16 GiB VM. The VM's RAM must exceed `limits.memory` plus the agent-host env and Incus overhead. With these limits set, every instance in the project must set `limits.cpu`, `limits.memory` and a root disk `size` itself (province always does). `restricted=true` also blocks unix-char device passthrough, so keep the site config's `tailscale.tun` at its default, `userspace`; `device` fails in this project (VMs need neither).

Smoke test:

```
incus launch images:debian/13 smoke --vm --project tasks \
  -c limits.cpu=1 -c limits.memory=1GiB -d root,size=10GiB
incus exec smoke --project tasks -- uname -a
incus delete smoke --force --project tasks
```

## 3. The agent-host env

Created from the owner's laptop with province, in the default project (not `tasks`), so task limits never squeeze it:

```
province env create agent-host --host <incus-host> --recipe <agent-recipe> --protect --json
province env create agent-host --host <incus-host> --recipe <agent-recipe> --protect --apply --json
```

The recipe should install `tmux`, `git`, `incus` (the client), `tofu`, `province` and Claude Code. `--protect` makes `env destroy` and `env gc` refuse it, so it cannot be destroyed by mistake; only the owner removes it, with `province env protect agent-host --off`. Never give it a `--ttl`.

## 4. Give the agent-host access to the tasks project only

On the Incus server, the owner creates a trust token restricted to the `tasks` project, in their own terminal (it prints the token; it must not land in the agent's transcript):

```
incus config trust add agent-host --projects tasks --restricted
```

In the agent-host env, the owner adds the remote in their own terminal, without `--token`: `incus remote add` then asks to confirm the server's certificate fingerprint and prompts `Trust token for tasks-box:`, where the owner pastes the token. It never appears on a command line, in shell history or in a process listing. Never pass it with `--token`.

```
incus remote add tasks-box https://<incus-vm-magicdns>:8443
incus list tasks-box: --project tasks
```

Its site config names that remote and project:

```yaml
hosts:
  - { name: tasks, backend: incus, remote: tasks-box, project: tasks, network: incusbr0, isolated: true }
```

`isolated: true` keeps task envs off the LAN and needs `network` set to a managed bridge (here the default `incusbr0`), the nftables firewall driver and Incus 6.0.4 or later; `env create` refuses otherwise (see `docs/incus.md`, "Isolated envs"). The province-task-envs skill stops any task whose env is not isolated.

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
