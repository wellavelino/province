---
name: province
description: Create, list, show, destroy and move Proxmox VE environments with the province CLI. Use when asked for a dev environment / container for a project, to check or remove one, or to move a guest between Proxmox hosts.
---

# province

`province` creates LXC environments on Proxmox VE (OpenTofu + SSH bootstrap + Tailscale) and moves existing guests between hosts.

## Rules

1. Never run province against real hosts unless the owner asked for that run in this conversation. A host may carry production.
2. Always pass `--json`. stdout then holds exactly one JSON document: the result, or `{"error": "...", "code": "refused|preflight|failed"}`. Progress and warnings go to stderr.
3. Exit codes: `0` ok; `2` refused or preflight (nothing was changed); `1` failed (something may have changed: read the error, it says how to recover).
4. Create in two steps: `province env create <name> --json` shows the plan; then `province env create <name> --apply --json` creates and bootstraps. Reuse the same flags (or none: sizes are remembered).
5. `env destroy` and `guest move` refuse without `--yes`. Only pass `--yes` when the owner asked for that destroy or move.
6. Never print, echo or log secret values. Pass them through the environment only.
7. Publishing to the internet (`env create --expose`, `env expose`) only when the owner asked for that env and port in this conversation; never on your own initiative, and never pass `--yes` to `env expose` otherwise. `env unexpose` is safe to suggest.

## Environment

- `PROVINCE_CONFIG`: path to the site file (or `--config <path>`).
- Proxmox API token per host, `user@realm!tokenid=secret`, in the variable the host's `token_env` names (default `PROXMOX_VE_API_TOKEN`). Standalone hosts each have their own token. A command needs the tokens of the hosts it touches: `guest move` needs both `--from` and `--to`; a missing one is refused (exit 2) with the variable and host named.
- `TS_AUTHKEY`: Tailscale auth key (needed by `env create --apply`).
- `TS_API_KEY`: optional Tailscale API key; lets `env destroy` delete the tailnet node. Without it the node stays listed offline.
- `TS_TAILNET`: optional, default `-`.

## Commands

```
province env create <name> [--host h] [--cpu n] [--ram MiB] [--disk GiB] [--docker] [--expose port] [--apply] --json
province env list --json
province env show <name> --json
province env destroy <name> --yes --json
province env expose <name> --port <p> --yes --json
province env unexpose <name> --yes --json
province guest move <vmid> --from <host> --to <host> [--to-vmid n] [--storage s] [--start] --yes --json
province guest prune <vmid> --host <host> [--force] --yes --json
```

- `--kind vm` is refused in v1 (exit 2).
- `env show` returns `ssh` (for example `ssh dev@demo`): use that line to reach the environment over the tailnet. Guests run Tailscale SSH by default (`tailscale.ssh: true`), so the tailnet policy decides who may log in; if a `check` rule asks for a browser re-authentication, stop and ask the owner to complete it. Plain SSH with the configured keys works on the LAN as a fallback.
- `public_url` in `env show` / `env list` is the env's public address when it is published (Tailscale Funnel). A `preflight` refusal from `env expose` that mentions `nodeAttrs` means the tailnet policy must change: show the owner the line from the error; do not edit the policy yourself.
- `env list` and `env show` mark an environment `live_status: "unknown"` with an `error` when its host is unreachable, gone from the config or missing its token; they still exit 0.

## Recovery

- `status: "bootstrap_failed"`: the container exists; fix the cause in the error and re-run `province env create <name> --apply --json`.
- A failed `guest move` leaves the source stopped; the error names the command that starts it again (`pct start <vmid>` or `qm start <vmid>` on the source host). After a failed restore the error also names the kept dump and the manual restore command. Do not run either, or retry the move, without the owner.
- Moving a province env's guest re-homes the env. If the result has `env.tofu_out_of_sync: true`, the move succeeded but the tofu state was not updated: show the owner the `env.fix` commands; `env destroy` refuses until they are done.
- After a successful move the source copy stays stopped as a rollback. Only when the owner says the moved copy is fine, run `province guest prune <vmid> --host <source> --yes --json`. It refuses (exit 2) a running, onboot=1, protected or locked guest, a province env, or one with no same-named copy on another host; never add `--force` without the owner.
