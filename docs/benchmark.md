# Benchmark: container spin-up

How long a Debian 13 container takes to become a usable env on a local container runtime, phase by phase. It is a baseline for the runtime, not a province number: province has no Docker or Apple backend, and the tofu apply and the tailnet join of a real `province env create --apply` are not in it.

```sh
go test -tags bench -run TestBenchContainerSpinup -v -timeout 60m ./internal/bootstrap/
```

- `PROVINCE_BENCH_RUNTIME`: `docker` (default), `podman`, `container` (Apple's, macOS) or `incus`. Only `docker` has been run; the others follow each tool's documented CLI and are untested.
- `PROVINCE_BENCH_RUNS`: number of runs (default 5).

## Phases

| phase | what is timed |
|---|---|
| start | start the container until `bash` answers and DNS resolves |
| base packages | `apt-get update`, `curl ca-certificates sudo openssh-server` (the package step of the bootstrap) and the user. The tailnet join and the sshd start are left out: they need a key and an init system |
| recipe (1st) | the real recipe script on a fresh env: git and `jq` from apt, Go 1.24 through mise, a `run` command. No dotfiles |
| recipe (2nd) | the same recipe again, as `env provision` does; everything is already in place |
| destroy | `rm -f` |

The image pull is timed once, outside the runs, and is labelled cold or cached (the bench never deletes an image to force a cold pull). Base packages and the first recipe run are almost all network time (apt mirrors, the Go download), so they depend on the connection more than on the runtime.

## Results

### Docker 29.8.2, Linux amd64 (WSL2), 16 CPUs, 5 runs, 2026-11-14

Image pull: 0.85 s, image already local (registry check only).

| phase | median | min | max |
|---|---|---|---|
| start | 0.39 s | 0.37 s | 0.40 s |
| base packages | 12.04 s | 11.72 s | 12.29 s |
| recipe (1st) | 7.61 s | 7.51 s | 9.37 s |
| recipe (2nd) | 0.15 s | 0.15 s | 0.16 s |
| destroy | 1.27 s | 1.06 s | 1.28 s |
| total | 21.48 s | 21.06 s | 23.44 s |

Not measured yet: Apple `container` (needs macOS), Incus (needs an Incus host), and the full province path on Incus or Proxmox (needs a host and a Tailscale key).
