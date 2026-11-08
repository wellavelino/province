<p align="center"><img src="docs/assets/banner.png" alt="Province: dev environments in minutes, for you and your agents" width="100%"></p>

# Province

**Dev environments in minutes, for you and your agents.**

Province spins up, moves and publishes development environments on your own Proxmox hosts with one command each, so you and your AI agents can go from idea to running pilot without touching the Proxmox UI.

Each environment runs in an LXC container, bootstrapped with SSH, Docker and Tailscale.

**Create. Connect. Move. Destroy.**

## How it works

- Each environment is declared with OpenTofu (`bpg/proxmox` provider), one state directory per environment.
- Bootstrap runs through the host (`pct exec`): your user and SSH keys, sudo, optional Docker, and Tailscale with Tailscale SSH.
- `guest move` moves any existing container or VM between standalone hosts, keeping its MAC address and so its IP.
- `--expose` publishes an environment's app at a public HTTPS URL through Tailscale Funnel.

Status: v1. LXC only (`--kind vm` is refused). Design notes: [docs/brief.md](docs/brief.md), [docs/plan-v1.md](docs/plan-v1.md). Agent instructions: [skills/province/SKILL.md](skills/province/SKILL.md).

## Install

```
go install github.com/wellavelino/province/cmd/province@latest
```

## Prerequisites

- **OpenTofu** (`tofu`) on `PATH` on the machine running province.
- **Template** on each host: `pveam update && pveam download local debian-13-standard_13.0-1_amd64.tar.zst` (or the one named in `defaults.template`).
- **API token** per host, for reads and for tofu, in the form `user@realm!tokenid=secret`. Standalone (non-clustered) hosts each have their own token: name its variable with `token_env` on the host (default `PROXMOX_VE_API_TOKEN`) and export one variable per host. A command needs only the tokens of the hosts it touches (`guest move` needs both). Give each token's role at least: `VM.Allocate`, `VM.Audit`, `VM.Config.CPU`, `VM.Config.Disk`, `VM.Config.Memory`, `VM.Config.Network`, `VM.Config.Options`, `VM.PowerMgmt`, `Datastore.AllocateSpace`, `Datastore.Audit`, `Sys.Audit`, `SDN.Audit`, `SDN.Use`. Without `SDN.Audit` the API hides bridges and province refuses with a hint.
- **SSH** from your machine to every host as root, or as a user with passwordless sudo (`sudo: true` in the config). province uses the system `ssh`, so `~/.ssh/config` aliases work. It runs `pct`, `qm`, `vzdump`, `zstd` and `df` there; `/dev/net/tun` and Docker's `keyctl` are set this way because an API token may not set them.
- **Tailscale**: `TS_AUTHKEY` (a tagged auth key for `tailscale.tag`) for `env create --apply`. Set `TS_API_KEY` too: tagged nodes do not expire, so without it a destroyed environment stays listed (offline) in the admin console. `TS_TAILNET` defaults to `-`. Step by step: [Tailscale setup](#tailscale-setup).

Secrets come only from the environment; every configured token variable is removed from province's environment once read, so child processes never inherit it. They never appear in arguments, generated files, tofu state or output (a redactor scrubs every line province prints).

## Site config

Keep it in your own private repo; pass it with `--config` or `PROVINCE_CONFIG`.

```yaml
state_dir: ~/infra/province            # one tofu dir per env; commit it to a private repo if you like
user: dev                              # sudo user created in each env (default: your OS user)
ssh_public_keys: [~/.ssh/id_ed25519.pub]
tailscale: { tag: "tag:dev", tun: userspace, ssh: true }
  # tun: userspace (default) | device (/dev/net/tun)
  # ssh: Tailscale SSH in each guest (default true); authorized_keys is installed either way
vmid_start: 100                        # env create picks the lowest VMID >= this that is free on every host
guest_locales: [en_US.UTF-8]           # generated in each guest so locales your ssh client forwards work (default)
defaults:
  host: pve1
  storage: local-lvm
  bridge: vmbr0
  template: local:vztmpl/debian-13-standard_13.0-1_amd64.tar.zst
  cpu: 2
  ram: 2048    # MiB
  disk: 16     # GiB
hosts:   # two standalone hosts, each with its own API token
  - { name: pve1, node: pve1, endpoint: "https://pve1.example:8006", insecure: true, ssh: pve1, sudo: false,
      dump_dir: /var/lib/vz/dump, token_env: PROVINCE_TOKEN_PVE1 }
  - { name: pve2, node: pve2, endpoint: "https://pve2.example:8006", ssh: admin@pve2.example, sudo: true,
      token_env: PROVINCE_TOKEN_PVE2 }
```

```
export PROVINCE_TOKEN_PVE1='province@pve!cli=…'   # from pve1
export PROVINCE_TOKEN_PVE2='province@pve!cli=…'   # from pve2
```

Unknown keys are rejected. `insecure` skips TLS verification for that host only. `token_env` must be a plain variable name; without it the host uses `PROXMOX_VE_API_TOKEN`. tofu gets the env's host token as `PROXMOX_VE_API_TOKEN` in its environment, never as an argument.

## Tailscale setup

Every guest joins your tailnet tagged (`tag:dev` below; use your own tag name) and, by default, with Tailscale SSH on, so your tailnet policy, not keys on the guest, decides who logs in. The user's `authorized_keys` is still installed as a fallback for plain SSH on the LAN. Do these once, in order, in the admin console:

1. **Create the tag.** Access controls → Tags → Create tag `tag:dev`, owned by admins. In the JSON policy file:
   ```json
   "tagOwners": { "tag:dev": ["autogroup:admin"] }
   ```
2. **Let your devices reach the guests.** The default policy (allow all) already does this. If you replaced it, add a grant (or the equivalent ACL rule):
   ```json
   "grants": [
     { "src": ["autogroup:member"], "dst": ["tag:dev"], "ip": ["*"] }
   ]
   ```
   Note that allow-all also lets the guests reach your devices; replace it with explicit grants if you want them isolated.
3. **Allow Tailscale SSH into the guests.**
   ```json
   "ssh": [
     { "action": "check", "src": ["autogroup:admin"], "dst": ["tag:dev"], "users": ["autogroup:nonroot", "root"] }
   ]
   ```
   `check` asks you to re-authenticate in the browser before a session (then remembers it for the check period, 12 h by default); `accept` lets the connection in with no prompt. `check` is safer for people; an unattended agent cannot answer the prompt, so use `accept` for a source that must log in without you (scope it tightly). `autogroup:nonroot` allows any non-root local user, such as the `user` from the site config.
4. **Auth key.** Settings → Keys → Generate auth key: reusable, pre-approved (matters only if device approval is on), not ephemeral, tags `tag:dev`. Export it as `TS_AUTHKEY`. It expires (at most 90 days); generate a new one when it does.
5. **API access token.** Settings → Keys → Generate access token. Export it as `TS_API_KEY`; `env destroy` uses it to delete the guest's node. It expires too.
6. **Verify** after `province env create demo --apply`:
   ```
   tailscale status | grep demo     # listed, tagged tag:dev
   ssh dev@demo                     # Tailscale SSH (MagicDNS name); a check rule opens a browser prompt
   ```
   Tailscale SSH is served by `tailscaled` itself, so it works with `tun: userspace`. Without MagicDNS use the `tailscale_ip` from `province env show demo`.

## Publishing an env (Tailscale Funnel)

Tailscale Funnel publishes one port of a guest on the public internet as `https://<name>.<tailnet>.ts.net/` (443, TLS from Tailscale) → `localhost:<port>` in the guest. Anyone with the URL can reach it. Once, in the admin console:

1. **MagicDNS and HTTPS certificates.** DNS page → enable MagicDNS, then HTTPS Certificates.
2. **Allow Funnel for the tag.** In the policy file:
   ```json
   "nodeAttrs": [
     { "target": ["tag:dev"], "attr": ["funnel"] }
   ]
   ```
   Without it (or without HTTPS) `env expose` refuses (exit 2) and prints this line for your tag.

Then per env:

3. **Publish.** Either at creation, `province env create demo --expose 3000 --apply` (recorded as `expose_port` in `province.json`, published right after the bootstrap; a plan without `--apply` only records it), or later `province env expose demo --port 3000 --yes`. The guest must be running. province checks the node's Funnel capabilities with `tailscale status --json`, then runs `tailscale funnel --bg 3000` in the guest (idempotent; a new port replaces the old one).
4. **The URL** is the guest's MagicDNS name: `public_url` in `env show` / `env list` (for example `https://demo.tail0000.ts.net/`). The first publish of a name can take a few minutes to resolve publicly.
5. **Unpublish.** `province env unexpose demo --yes` runs `tailscale serve --https=443 off` in the guest (everything served on 443 there stops) and clears the record. `env destroy` logs the node out, which ends Funnel too.

**Security.** A published app is reachable by anyone, so assume it can be compromised and keep it from reaching anything else. The allow-all default policy lets every node, including `tag:dev`, reach every other node; for exposed envs, replace it with grants where your devices reach the guests and `tag:dev` reaches nothing:

```json
{
  "tagOwners": { "tag:dev": ["autogroup:admin"] },
  "grants": [
    { "src": ["autogroup:member"], "dst": ["tag:dev"], "ip": ["*"] }
  ],
  "ssh": [
    { "action": "check", "src": ["autogroup:admin"], "dst": ["tag:dev"], "users": ["autogroup:nonroot", "root"] }
  ],
  "nodeAttrs": [
    { "target": ["tag:dev"], "attr": ["funnel"] }
  ]
}
```

No grant has `tag:dev` as its `src`, so a guest cannot open connections to your devices or the Proxmox hosts over the tailnet (replies to your own connections still flow). If envs must talk to each other, add only `{ "src": ["tag:dev"], "dst": ["tag:dev"], "ip": ["*"] }`. This covers the tailnet only: the guest also sits on the LAN through its bridge (`defaults.bridge`) and can reach the Proxmox hosts' LAN addresses there. Isolating that needs a separate VLAN or firewall, which province does not set up yet.

## Commands

Every command takes `--json`: stdout then holds exactly one JSON document, the result or `{"error":"…","code":"refused|preflight|failed"}`. Exit codes: `0` ok, `2` refused or preflight failed (nothing changed), `1` failed.

| Command | What it does |
|---|---|
| `env create <name> [--host h] [--cpu n] [--ram MiB] [--disk GiB] [--docker] [--expose port]` | Checks template, bridge and storage, writes `<state_dir>/<name>/main.tf.json` and shows the `tofu plan`. |
| `env create <name> --apply` | Applies, then (if needed) sets keyctl / `/dev/net/tun` over SSH and reboots, then bootstraps: user, keys, sudo, optional Docker, Tailscale. With an `expose_port`, then publishes it ([Funnel](#publishing-an-env-tailscale-funnel)). Prints `ssh <user>@<name>`. |
| `env list` | All environments with recorded and live status (one API call per host; an unreachable host, or one whose token variable is unset, shows `unknown`). |
| `env show <name>` | One environment, live status, the ssh line and the public URL if published. |
| `env expose <name> --port p --yes` | Publishes guest port `p` on the internet through Tailscale Funnel and records `expose_port` and `public_url`. Refuses without `--yes`, when the guest is not running, or when the tailnet does not allow Funnel for the tag. |
| `env unexpose <name> --yes` | Turns Funnel off in the guest and clears the record. |
| `env destroy <name> --yes` | `tailscale logout` in the guest, deletes the tailnet node (with `TS_API_KEY`), `tofu destroy`, removes the state dir. |
| `guest move <vmid> --from h1 --to h2 [--to-vmid n] [--storage s] [--start] --yes` | Read-only preflight (target VMID free on the target; if it is taken, the refusal suggests a `--to-vmid` free on every host, bridges, storage, free space for the full allocated disk size in the target storage; in `dump_dir`, room for the dump estimated from a running container's used bytes (used × 1.1 + 1 GiB), or the allocated size for a VM or a stopped container; on a `dir` or `zfspool` target storage, which usually shares the dump's filesystem, room for the dump estimate plus the allocated size), then: shut down the source, stream `vzdump --stdout --mode stop --compress zstd` through your machine into `dump_dir` on the target, `zstd -t`, restore with the same VMID or `--to-vmid` (MAC kept either way, so DHCP keeps the IP), set `onboot=0` on the source, remove the dump. The target starts only with `--start`, and only after the source is pinned to `onboot=0`. |
| `guest prune <vmid> --host h [--force] --yes` | Deletes the copy a move left behind (`pct destroy <vmid> --purge` / `qm destroy <vmid> --purge`). Refuses (exit 2, nothing changed) unless the guest is stopped, has `onboot=0`, has no `protection=1` or lock, is not a province env (use `env destroy`), and a guest with the same name exists on another configured host (the moved-copy check; `--force` skips only that check). |

The moved guest keeps the `onboot` value it had in the archive; the source is kept, stopped with `onboot=0`, as a rollback. Once you are happy with the moved copy, remove the source with `province guest prune <vmid> --host <source> --yes`.

## Recovery

- **Bootstrap failed** (`status: bootstrap_failed`): the container and its state are kept. Fix the cause and re-run `province env create <name> --apply`; apply is a no-op if nothing changed and the bootstrap is idempotent.
- **tofu apply failed**: fix the cause (often a token privilege) and re-run `province env create <name> --apply`.
- **After a move**: the source copy stays (stopped, `onboot=0`) until you run `province guest prune <vmid> --host <source> --yes`; to roll back instead, stop the new copy and `pct start <vmid>` / `qm start <vmid>` the source.
- **Destroy failed**: the state dir is kept; re-run `province env destroy <name> --yes`.
- **Move failed after the source stopped**: nothing runs on the target; the error names the command to start the source again (`pct start <vmid>` or `qm start <vmid>` on the source host). If the restore failed, the verified dump is kept in `dump_dir` on the target and the error gives its path: fix the cause (often storage), check `pct list` / `qm list` for a partial guest, then restore by hand (`pct restore <vmid> <dump> --storage <s>` or `qmrestore <dump> <vmid> --storage <s>`) without a second transfer, or delete the dump. If only `onboot=0` on the source failed, the target restore succeeded: set it by hand (`pct set <vmid> --onboot 0`) before starting the target; do not re-run the move.
- **Moved a province env**: after a successful `guest move` of an env's guest, province updates its `province.json` and `main.tf.json` (new host, node, VMID, storage) and re-points the tofu state with the target host's token (`tofu state rm proxmox_virtual_environment_container.env`, then `tofu import proxmox_virtual_environment_container.env <node>/<vmid>`). If that fails, the move still stands (the guest is safe), the env is marked `tofu_out_of_sync` and the output lists the exact commands still to run; `env destroy` refuses until they are done.

## Development

`bash .claude/verify.sh` runs gofmt, vet, build, `go mod tidy -diff`, race tests, shellcheck and staticcheck (when installed). Tests use fakes only; none talk to a real host.

License: [MPL-2.0](LICENSE).
