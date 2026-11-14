# To do

What province should get next, in order, and what it deliberately does not get. Update this file when something is finished or the order changes.

## The rule every item is held to

Province does three things to one kind of object, the env:

1. **Create** it on a host you own.
2. **Connect** it to your tailnet.
3. **Configure** it with a recipe.

Anything else belongs to the host platform (Proxmox, Incus) or to the user's recipe. A new feature has to fit one of the three, stay small, and work the same on every backend, or it does not go in. VMs are the stated exception: `--kind vm` works on Incus first, and Proxmox follows (planned, below).

## 1. Prove what exists

Unit tests use fakes. These run the real thing.

- [x] **Generated tofu configs against the real providers.** `scripts/tofu-validate.sh`, CI job `tofu-validate`.
- [x] **Recipes in a real Debian 13 container.** `internal/bootstrap/recipe_container_test.go` (`-tags integration`), CI job `recipe-container`: real apt, mise and git, run twice; checks the package, the tool on `PATH` as sshd and a login shell see it, the dotfiles clone and `install.sh`, and the `run` command.
- [x] **Plan against a real Incus.** `scripts/e2e-incus.sh`, CI job `incus`: the real `incus query` preflight and a real `tofu plan` on every push.
- [x] **Full lifecycle on a real Incus.** First green run with the secrets: CI run 43383561228, attempt 2 (VM on KVM included). The same script, in CI on Incus 6.0 LTS from the [Zabbly](https://github.com/zabbly/incus) apt repo (key pinned by fingerprint; Ubuntu's own 6.0.0 cannot do `isolated`) with the nftables firewall driver. It uses two envs: an isolated container created with `--protect --ttl 1s`, and a `--kind vm` with `--ttl 1s` (a container instead, with a message, when the runner has no `/dev/kvm`). Without secrets it plans both (the real preflight, isolated checks included), runs `env list`, `show` and `env gc` on the plans, and checks that `env destroy` refuses the protected env (exit 2) until `env protect --off`. With the secrets it goes on to create, bootstrap, recipe, show, provision and destroy both; probes from inside the isolated env that the runner's own LAN address and the bridge gateway (on a non-DNS port) are unreachable while DNS and HTTPS to the internet work, with the VM as the control that reaches both; and checks that both envs show as expired, that `env gc` without `--yes` changes nothing and that `env gc --yes` destroys the VM and skips the protected env. Every env the run made is destroyed on exit, also on failure. The Tailscale secrets reach only the e2e step, and only on pushes to `main` and pull requests from branches of this repo; fork pull requests get none. Needs these Actions settings:
  - secret `TS_AUTHKEY`: a tagged Tailscale auth key used only for CI (reusable, ephemeral is fine);
  - secret `TS_API_KEY`: so destroy removes the tailnet node;
  - variable `TS_TAG`: the key's tag, if it is not `tag:dev`.
- [ ] **Full lifecycle on a real Proxmox host.** No CI equivalent (nested Proxmox needs KVM). By hand, on a spare, non-production host: `env create demo --recipe <r>`, `--apply`, `ssh`, `env provision demo`, `env destroy demo --yes`. Write down what broke.
- [x] **Security audit.** First run done on commit 67599ed: no critical, high or medium issue; four low ones confirmed. Known issues below.

## 1b. Known issues from the first security audit

These are known bugs and weaknesses, listed in the open. Checked items are fixed on `main`; the rest are still open. None is rated above low. If you run province, keep its site config and state private, and read open items before relying on the feature they name.

Confirmed (low):

- [x] `guest move --start`: when the final start fails, still re-home the env (or mark it out of sync). Until fixed, move province envs without `--start` and start them by hand.
- [x] Before acting on a Proxmox env's guest (expose, provision, destroy), check that the guest at its VMID is named after the env; count VMIDs recorded in the state dir as used. Same check for Incus via `user.province.env`.
- [x] `env destroy`: delete the tailnet device by an ID stored at bootstrap, not by hostname; skip the delete for an env that never joined.
- [x] `env expose`: accept the guest's reported name only if it ends in `.ts.net` and starts with the env name; escape control characters in text output.

Cheap fixes for leads that need an outside fact to confirm:

- [x] CI: set the Tailscale secrets on the e2e step only, run that job only on pushes to `main`, pin actions by commit SHA.
- [x] `env create --apply`: refuse an env marked `tofu_out_of_sync`.
- [ ] Re-home after a move: validate the env name and use the directory that matched.
- [ ] Print the MagicDNS name the node actually got, not the bare env name, in the `ssh` hint.
- [x] Escape `${` and `%{` in generated tofu strings; accept only real public-key lines from `ssh_public_keys`.
- [x] Put guest error text in its own `--json` field and tell agents in SKILL.md that it is data.

Hardening:

- [x] Bootstrap: join the tailnet before installing Docker, then `unset TS_AUTHKEY`; don't resend the key once the node has joined.
- [x] `.gitignore`: `province.yaml`, `*.tfstate*`, `.terraform/`, `.env*`, before going public.
- [x] Add `govulncheck` to verify and CI.

Not examined yet (second audit run): restoring a move archive from a guest that ran untrusted code; hosts with `insecure: true` while a guest shares their LAN.

## 2. One clean backend seam

- [x] `internal/env/backend.go`: one interface, a Proxmox and an Incus implementation. The lifecycle is written once against it.

## 3. `isolated: true`

Today every env can reach the LAN it sits on, including the hosts themselves. An isolated env can reach the internet and the tailnet but not the LAN, enforced on the host so the env's own sudo user cannot undo it.

- [ ] Proxmox: the guest firewall, declared in the tofu config. Deferred (owner decision, 2026-11-13): envs use DHCP, so province does not know the gateway or DNS server to keep allowed; `isolated: true` on a Proxmox host is refused when the site config loads.
- [x] Incus: a network ACL, declared in the tofu config (`isolated: true` on the host, see [docs/incus.md](incus.md#isolated-envs)).
- [ ] Later, if needed: an allowlist of outbound hosts instead of "everything but the LAN".

## 4. Disposable and long-lived, as two flags

Long-lived is already the default: an env stays until it is destroyed and starts with its host.

- [x] `--ttl 24h`: recorded with the env; `env list` shows what has expired and `env gc --yes` destroys it. No daemon: run `env gc` from cron or an agent.
- [x] `--protect`: `env destroy` refuses until the flag is removed.

Backups of long-lived envs stay with the platform (Proxmox backup jobs, `incus export`).

## 5. VMs (`--kind vm`)

A stronger wall than a container: the env has its own kernel.

- [x] Incus: `type = "virtual-machine"` in the tofu config, the kind recorded with the env, a wait for the VM's agent before the bootstrap.
- [ ] Proxmox: needs a cloud-init template with qemu-guest-agent. Until then `--kind vm` on a Proxmox host is refused.

## Not building, for now

Each of these was considered and set aside. Reopen one only with a concrete need.

- **A credential proxy** that adds tokens to requests as they leave the env. Use short-lived, narrowly scoped tokens instead.
- **Snapshots and `env reset`.** Recreating from a recipe takes minutes.
- **More backends** (Docker, cloud VMs, NixOS hosts).
- **Nix, Ansible or cloud-init as something province knows about.** A recipe's `run` can call any of them.
- **A private dotfiles repo.** Needs a credential in the guest; wait for a design that keeps it out of state and config.
