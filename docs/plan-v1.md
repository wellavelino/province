# province v1: implementation plan (2026-11-08)

Produced by the planning pass over `docs/brief.md`. Implement in this order, test first in each step, commit to `main` after each green step.

## Verified facts (sources checked 2026-11-08)

- bpg `proxmox_virtual_environment_container` has: `unprivileged`, `features{nesting,keyctl}`, `operating_system{template_file_id,type}`, `disk{datastore_id,size}`, `cpu{cores}`, `memory{dedicated,swap}`, `network_interface{name,bridge}`, `initialization{hostname, ip_config{ipv4{address="dhcp"}}, user_account{keys}}`, `start_on_boot`, `started`, `tags`, `device_passthrough`. Provider reads `PROXMOX_VE_API_TOKEN`, `PROXMOX_VE_ENDPOINT`, `PROXMOX_VE_INSECURE` from env; token format `user@realm!id=secret`. Creating/deleting containers needs no provider SSH. Latest release v0.120.0 (2026-11-02). Bridges are invisible to the API unless the token role has `SDN.Audit`/`SDN.Use`.
- pve-container: `dev\d+` config raises "configuring device passthrough is only allowed for root@pam". So `/dev/net/tun` is set with `pct set <vmid> --dev0 /dev/net/tun` over SSH, never via tofu.
- `pct restore <vmid> -` (stdin) requires `--rootfs`. Use a temp file on the target for both lxc and qemu (keeps the archive config, allows `zstd -t`, allows a free-space preflight).
- `PVE::Storage::archive_info` standard names: `vzdump-<type>-<vmid>-YYYY_MM_DD-HH_MM_SS.(tar|vma).zst`. Use that form.
- `vzdump --mode stop` restarts a guest that was running, so `move` stops the source explicitly first.
- `tailscale up --auth-key file:/path` works. `tailscale logout` only expires the login; tagged non-ephemeral nodes linger until deleted via API.
- Toolchain: go 1.27.1, gcc (`-race` works), shellcheck. No tofu, no staticcheck locally. Cached modules: cobra v1.10.2, pflag v1.0.9, `go.yaml.in/yaml/v3` v3.0.4 (`go mod download` may need network).

## Approach

- CLI: cobra. Persistent `--json` and `--config` (fallback env `PROVINCE_CONFIG`).
- YAML: `go.yaml.in/yaml/v3`, decode with `KnownFields(true)`.
- Transports: reads via the Proxmox HTTP API (`Authorization: PVEAPIToken=<token>`, per-host `insecure`); mutations via the system `ssh` binary (so `~/.ssh/config` aliases work), running `pct`/`qm`/`vzdump`, optional `sudo -n`.
- VM kind: LXC-only v1. `--kind vm` fails with exit 2 before any I/O. `guest move` handles `lxc` and `qemu` (existing guests).
- Secrets only from env: `PROXMOX_VE_API_TOKEN`, `TS_AUTHKEY`, optional `TS_API_KEY`, `TS_TAILNET` (default `-`). Token reaches tofu via `Cmd.Env` only. The auth key reaches the guest as a shell-quoted `TS_AUTHKEY='…'` line prepended to the embedded script on `pct exec … bash -s` stdin, written to `/run/province.authkey` (0600), passed as `--auth-key file:…`, then deleted. A central redactor scrubs secret values from every stdout/stderr/error. Secrets never appear in argv on any machine, in generated files or in tofu state.
- State: one tofu dir per env at `<state_dir>/<name>/`: `main.tf.json`, local `terraform.tfstate`, `.terraform.lock.hcl`, `.gitignore` (`.terraform/`), `province.json` metadata (name, kind, host, node, vmid, status `planned|applied|bootstrapped|bootstrap_failed`, docker, tun, tailscale_ip, timestamps). `vm_id` is left to the provider and read back from `tofu output -json`.
- Exit codes: 0 ok; 1 failure; 2 refused/usage/preflight. With `--json`, exactly one JSON document on stdout: the result, or `{"error":"…","code":"refused|preflight|failed"}`.

## Package layout (module `github.com/wellavelino/province`)

```
cmd/province/main.go              os.Exit(cli.Main(os.Args[1:], os.Stdout, os.Stderr))
internal/cli/                     root.go env.go guest.go output.go exit.go; Deps struct injected for tests
internal/config/                  config.go (types, Load, expand ~), validate.go (ValidName, Validate), testdata/
internal/secrets/                 env.go (FromEnv), redact.go (Redactor)
internal/run/                     runner.go (Cmd, Runner, Local, ExitError); runtest/fake.go
internal/sshx/                    ssh.go (Target, Command, Quote)
internal/tfgen/                   lxc.go (structs -> main.tf.json), ostype.go; testdata/*.golden.json
internal/tofu/                    tofu.go (Tofu iface, Exec impl on Runner)
internal/proxmox/                 client.go (Client iface, HTTP impl), types.go, guestconf.go; proxmoxtest/fake.go
internal/hostops/                 host.go (Host iface, SSHHost impl); hostopstest/fake.go
internal/bootstrap/               bootstrap.sh (go:embed), payload.go, result.go
internal/tailscale/               api.go (DeleteDeviceByHostname)
internal/env/                     metadata.go create.go list.go show.go destroy.go (Manager)
internal/move/                    preflight.go move.go dumpname.go
skills/province/SKILL.md
.claude/verify.sh
```

## Interfaces (fakes implement these)

```go
// internal/run
type Cmd struct { Name string; Args []string; Dir string; Env []string; Stdin io.Reader; Stdout, Stderr io.Writer }
type Runner interface {
    Run(ctx context.Context, c Cmd) error           // non-zero -> *ExitError{Code, Stderr}
    Pipe(ctx context.Context, src, dst Cmd) error   // src.Stdout -> dst.Stdin; error if either fails
}

// internal/sshx
type Target struct { Dest string; Sudo bool }
func Quote(s string) string                         // POSIX single-quote quoting
func Command(t Target, remote ...string) run.Cmd    // ssh -o BatchMode=yes -o ConnectTimeout=15 -o ServerAliveInterval=30 -- dest [sudo -n] quoted...

// internal/tofu
type Tofu interface {
    Init(ctx context.Context, dir string) error
    Plan(ctx context.Context, dir string) (text string, changes bool, err error) // -detailed-exitcode
    Apply(ctx context.Context, dir string) error
    Destroy(ctx context.Context, dir string) error
    Output(ctx context.Context, dir string) (map[string]json.RawMessage, error)
}

// internal/proxmox (API, read-only)
type Client interface {
    ListGuests(ctx context.Context, node string) ([]Guest, error)   // /nodes/{n}/lxc + /qemu
    GuestStatus(ctx context.Context, node, typ string, vmid int) (string, error)
    GuestConfig(ctx context.Context, node, typ string, vmid int) (map[string]string, error)
    Storages(ctx context.Context, node string) ([]Storage, error)   // name, content, avail
    Bridges(ctx context.Context, node string) ([]string, error)     // /network?type=any_bridge
    TemplateExists(ctx context.Context, node, volid string) (bool, error)
}

// internal/hostops (SSH, mutating; one value per host)
type Host interface {
    Exec(ctx context.Context, vmid int, stdin io.Reader, stdout io.Writer, argv ...string) error // pct exec vmid -- argv
    Set(ctx context.Context, typ string, vmid int, opts ...string) error                         // pct set | qm set
    Shutdown(ctx context.Context, typ string, vmid int, timeoutSec int) error                    // then verify stopped
    Stop(ctx context.Context, typ string, vmid int) error
    Start(ctx context.Context, typ string, vmid int) error
    Reboot(ctx context.Context, typ string, vmid int) error
    Status(ctx context.Context, typ string, vmid int) (string, error)
    DumpCmd(typ string, vmid int) run.Cmd           // vzdump vmid --stdout --mode stop --compress zstd
    ReceiveCmd(path string) run.Cmd                 // sh -c 'umask 077; cat > <quoted>'
    ZstdTest(ctx context.Context, path string) error
    DfAvail(ctx context.Context, path string) (int64, error) // df -P -B1
    Remove(ctx context.Context, path string) error
    Restore(ctx context.Context, typ string, vmid int, archive, storage string) error // pct restore | qmrestore
}
```

## Site config schema

```yaml
state_dir: ~/infra/province
user: dev
ssh_public_keys: [~/.ssh/id_ed25519.pub]
tailscale: { tag: "tag:dev", tun: userspace }      # userspace | device
defaults: { host: pve1, storage: local-lvm, bridge: vmbr0,
            template: local:vztmpl/debian-13-standard_13.0-1_amd64.tar.zst, cpu: 2, ram: 2048, disk: 16 }
hosts:
  - { name: pve1, node: pve1, endpoint: https://pve1.example:8006, insecure: true, ssh: pve1, sudo: false, dump_dir: /var/lib/vz/dump }
```

## Steps and the tests that prove them

1. **Skeleton + gate.** `go.mod`, `cmd/province/main.go`, `internal/cli/root.go` (`env`/`guest` groups, `--json`, `--config`). `.claude/verify.sh`: `gofmt -l` check, `go vet`, `go build`, `go mod tidy -diff`, `CGO_ENABLED=1 go test -race -count=1 ./...`, `shellcheck -s bash internal/bootstrap/bootstrap.sh .claude/verify.sh`, `staticcheck` if on PATH else a printed skip. Test: `--json` error shape and exit 2 for `env create x --kind vm`.
2. **sshx.** `Quote` (empty, spaces, `'`, `$`, backticks, newline, a real `ssh-ed25519 AAAA…+/= comment` key); `Command` (argv order; `sudo -n` only when `Sudo`; every remote arg quoted; `BatchMode=yes`).
3. **config.** Full `testdata/site.yaml` loads; unknown key rejected; missing `hosts`/`state_dir` rejected; `~` expanded in `state_dir` and `ssh_public_keys`; `ValidName` accepts `demo`, `proj-x1`, rejects `Demo`, `../x`, `a_b`, 64 chars, empty; defaults (`tailscale.tun=userspace`, `dump_dir=/var/lib/vz/dump`, `user` falls back to the OS user); `HostByName` errors on unknown; `DefaultHost` errors when ambiguous.
4. **secrets.** `FromEnv` requires `PROXMOX_VE_API_TOKEN` in `user@realm!id=secret` form and names the missing var; `Redactor.Scrub` replaces each secret with `[redacted]`.
5. **run.** `Local` with harmless binaries only: `sh -c 'exit 3'` → `*ExitError{Code:3}` with stderr; `Pipe(printf abc, cat)` delivers `abc`; `Pipe` with a failing src (`printf abc; exit 3`) errors (a truncated dump is detectable); context cancel kills the child. `runtest.Fake`: scripted by argv prefix, records argv/env/stdin, fails on unexpected commands.
6. **tfgen.** Golden `main.tf.json` for default LXC, `--docker` (nesting+keyctl), `tun: device` (adds `lifecycle.ignore_changes: ["device_passthrough"]`). Asserts: never a `device_passthrough` block, no `api_token`, pinned provider version, `output.vmid`, deterministic keys; `OSType` debian/ubuntu/unmanaged; `ip_config` list with `ipv4.address="dhcp"`. `nesting` on for every env (systemd needs it; PVE UI default), `keyctl` only with `--docker`.
7. **tofu.** Fake-runner argv (`-input=false`, `-no-color`, `-auto-approve` only on apply/destroy, `-detailed-exitcode` on plan with exit 2 → `changes=true`); `Cmd.Env` has `PROXMOX_VE_API_TOKEN`, `TF_IN_AUTOMATION=1`, and not `TS_AUTHKEY`/`TS_API_KEY`; token never in argv; `Output` parses `{"vmid":{"value":105}}`; missing `tofu` → error naming the install step (`exec.LookPath` up front).
8. **proxmox.** `httptest`: auth header; `insecure` per host only; `ListGuests` merges lxc+qemu with `Type`; 401 → error mentioning the token role; empty `Bridges` → hint about `SDN.Audit`; config parsing: `ParseSize("8G")`, rootfs/mpN/scsiN/virtioN sizes (skip `media=cdrom`), bridges from `netN`, passthrough keys (`devN`, `usbN`, `hostpciN`, `lxc.*`).
9. **hostops.** Fake runner: `Exec` → `ssh … -- pct exec <vmid> -- bash -s` with stdin; `Set` → `pct set 105 --dev0 /dev/net/tun`; `qm` variants for `qemu`; `Shutdown` = `pct shutdown --timeout N` then `Status`, error if still running; exact `DumpCmd`/`ReceiveCmd` argv (`umask 077`, quoted path); `Restore` lxc `pct restore <vmid> <archive> --storage s`, qemu `qmrestore <archive> <vmid> --storage s`; `DfAvail` parses `df -P -B1`; sudo prefix propagates.
10. **bootstrap.** `bootstrap.sh`: functions only, last line `main "$@" </dev/null`; `: "${TS_AUTHKEY:?}"` checks; wait for `getent hosts deb.debian.org` (120 s); apt `curl ca-certificates sudo openssh-server`; idempotent user/sudoers/authorized_keys; optional Docker via `get.docker.com`; Tailscale via `install.sh`; `FLAGS="--tun=userspace-networking"` in `/etc/default/tailscaled` when `P_TS_TUN=userspace`; key file 0600, `tailscale up --auth-key file:… --hostname … --advertise-tags …`, `rm -f`; last line printed `PROVINCE_TS_IP=<ip>`. Tests: payload = quoted assignments + script byte-for-byte; a key containing `'` round-trips through real `sh`; `ParseResult` extracts the IP and errors if absent; static check of the script's last line.
11. **env create.** Fakes for Tofu, Client, Host, `t.TempDir()` state: invalid name → zero calls; existing env → refused; preflight (template/bridge/storage missing) → exit 2 before `tofu init`; plan path writes `main.tf.json`, `.gitignore`, metadata `planned`, calls Init+Plan only; apply path Init→Apply→Output→`applied` with vmid→bootstrap Exec→`bootstrapped` with `tailscale_ip`; bootstrap failure → `bootstrap_failed`, dir kept, error says re-run `create --apply`; re-run on `bootstrap_failed` re-runs apply+bootstrap; `tun: device` → `Set(--dev0)` then `Reboot` before `Exec`; `TS_AUTHKEY` only in Exec stdin.
12. **env list/show.** One API call per distinct host; unreachable host → `live_status:"unknown"` + `error`, exit 0; show = metadata + live status + `ssh <user>@<name>` hint; unknown env → exit 1.
13. **tailscale.** `httptest`: `GET /api/v2/tailnet/-/devices` with Bearer, match by `hostname` (or `name` prefix `<name>.`), `DELETE /api/v2/device/<id>`; not found → warning.
14. **env destroy.** No `--yes` → exit 2, zero calls; order: `Exec(tailscale logout)` (best effort) → API delete if `TS_API_KEY` else warning → `Destroy` → remove dir; `Destroy` failure keeps the dir, exit 1; `planned` env skips the tailscale steps.
15. **move.** `DumpName("lxc",105,t)=="vzdump-lxc-105-2026_11_08-10_15_00.tar.zst"`, qemu `.vma.zst`; preflight refusals with zero mutations: `--from==--to`, source missing, VMID on target, missing bridge, missing storage, free space below total disk size (dump dir or target storage); `devN/usbN/hostpciN` → warnings; happy order: Shutdown(src) → Pipe(Dump, Receive) → ZstdTest → Restore → Set(src `--onboot 0`) → Remove(dump); target never started without `--start`, and with `--start` only after `--onboot 0` succeeded; Pipe error → remove partial dump, source stopped, message contains `pct start <vmid>`, exit 1; Restore error → same, no target start; `--onboot 0` failure → target not started, exit 1; already-stopped source → no Shutdown. `--storage` defaults to the source rootfs storage if present on the target.
16. **CLI wiring.** Through cobra with `Deps`: `--json` success shape per command; a secret in a fake error is redacted in both modes; `--yes` on destroy/move; `--config` vs `PROVINCE_CONFIG`; exit codes.
17. **Docs.** `skills/province/SKILL.md` (always `--json`; plan then `--apply`; `--yes`; env vars; never run against real hosts without the owner's request; read `env show` for the ssh line). README: prerequisites (tofu on PATH, `pveam download` the template, API token role incl. `SDN.Audit`, root or passwordless-sudo SSH to hosts, `TS_API_KEY` because tagged nodes don't expire), config example, command reference, recovery (`create --apply` re-run; `pct start` after a failed move).

## Assumptions

1. `nesting` always on; `keyctl` only with `--docker`.
2. Not enough free space for a move is a refusal (exit 2), no `--force` in v1.
3. Docker from `get.docker.com`.
4. Per-host `dump_dir`, default `/var/lib/vz/dump`, standard vzdump filename.
5. Recovery from a failed bootstrap is re-running `env create --apply`.

## Open risks

- Unverified: inbound SSH to a userspace-networking node; `vzdump --stdout` honoring `--compress zstd`; `pct exec … bash -s` stdin forwarding; `dev0: /dev/net/tun` on an unprivileged CT.
- No tofu locally: the wrapper is argv-tested only; the first live `create` is the real integration test.
- The dump transits the operator machine (no resume).
- Without `TS_API_KEY`, destroyed envs stay listed offline in the tailnet admin console.
- Tests use example hostnames only (`pve1.example`), never site data.
