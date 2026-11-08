# province: brief for the first plan (2026-11-08)

Input for the first implementation plan. This repo is private for now and will go public later, so write it as public from day one: site details (IPs, hostnames, device lists, secrets) belong in each user's own private config repo, never here.

## Goal

An agent can say "create an environment for project X" and get a running LXC or VM it can reach over SSH and Tailscale, without a human clicking through the Proxmox UI. The same tool can move an existing guest from one Proxmox host to another.

## Interface (agent-first)

- A CLI, `province`, with `--json` output on every command and non-zero exits on failure.
- A Claude Code skill (`SKILL.md`) that tells an agent when and how to call it. An MCP server can come later if the CLI proves too loose.
- Commands:
  - `env create <name> [--kind lxc|vm] [--cpu] [--ram] [--disk] [--docker]`
  - `env list`, `env show <name>`
  - `env destroy <name> --yes`
  - `guest move <vmid> --from <host> --to <host> [--start-order …] --yes`
- Safe by default: `create` shows the plan first, unless `--apply` is given. `destroy` and `move` refuse to run without `--yes`. Output never prints a secret.

## Configuration

- A site file (e.g. `province.yaml`) lives outside this repo, in the user's private repo; its path comes from a flag or an env var. It names the hosts, the API endpoint, storage, bridge, templates, SSH public key files, the Tailscale tag and the defaults.
- Secrets (Proxmox API token, Tailscale auth key) come from env vars or SOPS + age files, never from the repo.
- Keep the Tailscale auth key out of OpenTofu state: pass it in at bootstrap over SSH, not as a tofu variable.

## Pieces

1. OpenTofu module(s) for an LXC and a VM (`bpg/proxmox`): CPU, RAM, disk, NIC, template, `onboot`, `features: nesting=1,keyctl=1` for Docker.
2. Bootstrap: user, `authorized_keys`, sudo, Docker (optional), `tailscale up --authkey … --advertise-tags=tag:<tag> --hostname=<name>`.
3. Tailscale inside an unprivileged LXC needs `/dev/net/tun`. First task: a prototype comparing PVE device passthrough (`dev0: /dev/net/tun`) with `--tun=userspace-networking`, then pick one.
4. Guest move: `vzdump --stdout` from the source, streamed over SSH into `pct restore` / `qmrestore` on the target with the same VMID. The MAC is kept, so DHCP keeps the IP. Restore stopped with `onboot=0`; refuse if the VMID already exists on the target; stop on the source before starting on the target, so both copies never run at once.

## Open questions for the plan

1. CLI language: Go (single binary), Python, or a thin shell wrapper around `tofu`?
2. OpenTofu state: one state per environment or one per site? Where does it live (the private repo, encrypted)?
3. Does `bpg/proxmox` set LXC device passthrough with an API token, or does it need `root@pam`?
4. How does the tool reach the hosts: Proxmox API only, or API plus SSH (needed for streaming backups during a move)?
5. Removing the tailnet node on `destroy` needs a Tailscale API key. Is that worth it, or is `tailscale logout` from inside the guest before deleting it enough?

## Non-goals (for now)

Proxmox clustering, HA, public cloud providers, a web UI.

## First milestone

`province env create demo --apply` on one host creates an LXC; `ssh <user>@demo` works over Tailscale; `province env destroy demo --yes` removes it and its tailnet node.
