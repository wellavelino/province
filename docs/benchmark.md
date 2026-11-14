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
| base packages | `apt-get update`, then `curl ca-certificates sudo git` and the user: what the bootstrap installs before a recipe |
| recipe (1st) | the real recipe script: `jq` from apt, Go 1.24 through mise, a dotfiles clone, a `run` command |
| recipe (2nd) | the same recipe again, as `env provision` does; everything is already in place |
| destroy | `rm -f` |

The image pull is timed once, outside the runs. Base packages and the first recipe run are almost all network time (apt mirrors, the Go download), so they depend on the connection more than on the runtime.

## Results

### Docker 29.8.2, Linux amd64 (WSL2), 16 CPUs, 5 runs, 2026-11-14

Image pull: 3.13 s (once).

| phase | median | min | max |
|---|---|---|---|
| start | 0.42 s | 0.41 s | 0.85 s |
| base packages | 10.27 s | 10.10 s | 10.42 s |
| recipe (1st) | 8.92 s | 8.37 s | 11.36 s |
| recipe (2nd) | 0.18 s | 0.17 s | 0.18 s |
| destroy | 1.45 s | 1.04 s | 2.40 s |
| total | 21.11 s | 20.63 s | 25.07 s |

Not measured yet: Apple `container` (needs macOS), Incus (needs an Incus host), and the full province path on Incus or Proxmox (needs a host and a Tailscale key).
