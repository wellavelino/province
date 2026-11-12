# Incus hosts

Province can run environments on [Incus](https://linuxcontainers.org/incus/) as well as on Proxmox VE. Incus runs the same kind of LXC containers on any Linux machine, so a spare box or your own workstation is enough.

On an Incus host, an env is one Incus container, or with `--kind vm` one Incus virtual machine. OpenTofu declares it (provider `lxc/incus`), and province runs the bootstrap and recipes through `incus exec`. Everything inside the guest is the same as on Proxmox: your user and SSH keys, sudo, optional Docker, Tailscale, [recipes](recipes.md) and Funnel.

## What you need

- **The `incus` client** on the machine that runs province, next to `tofu`.
- **An Incus remote** for each Incus host, added once with `incus remote add`. Province, tofu and the `incus` client all authenticate through it, so no Incus secret passes through province.
- A storage pool on the server (`default` unless you set `storage_pool`), and a profile or network that gives containers a NIC with internet access.

## Set up the remote

On the Incus server, once:

```
incus config set core.https_address :8443
incus config trust add province          # prints a one-time trust token
```

On the machine that runs province:

```
incus remote add box https://box.example:8443 --token <token>
incus list box:                          # check that it answers
```

This stores a client certificate in your Incus client config (`~/.config/incus`). Treat that directory like an SSH key.

If the Incus server is on your tailnet, use its MagicDNS name in the address (`https://box:8443`) so province works from anywhere you are, not just the LAN.

For Incus on the same machine as province, skip this: use the built-in `local` remote (your user needs to be in the `incus-admin` group).

## Site config

```yaml
hosts:
  - name: box
    backend: incus
    remote: box                 # the name from `incus remote add`
    project: default            # optional: an Incus project to keep envs in
    storage_pool: default       # optional, default "default": the root disk's pool
    network: incusbr0           # optional: attach eth0 to this network instead of the profile's NIC
    image: images:debian/13     # optional, default images:debian/13
```

Hosts without `backend` are Proxmox hosts, so existing site files keep working. Settings of the other backend are refused on a host (for example `token_env` on an Incus host, or `remote` on a Proxmox host). A site with only Incus hosts needs no `defaults.storage`, `defaults.bridge` or `defaults.template`; `defaults.cpu`, `ram`, `disk` and `recipe` apply to both backends.

## Use it

With the remote added and the host in your site config, the rest of [Getting started](../README.md#getting-started) (Tailscale, then `env create`) is the same as on Proxmox:

```
province env create api --host box --recipe go
province env create api --host box --recipe go --apply
province env list
```

Before tofu runs, province checks that the remote answers and has the storage pool (and the network, if set). The env's guest is named after the env; `env show` lists it as `instance` instead of `vmid`.

| Command | On Incus |
|---|---|
| `env create`, `list`, `show`, `destroy` | Yes |
| `env provision` (recipes) | Yes |
| `env expose`, `unexpose` (Funnel) | Yes |
| `--docker` | Yes: sets `security.nesting` and the `mknod` and `setxattr` syscall interception in the tofu config |
| `tailscale.tun: device` | Containers: passes `/dev/net/tun` through as a `unix-char` device. VMs: refused (exit 2) |
| `--kind vm` | Yes: see [VMs](#vms) |
| `guest move`, `guest prune` | No: refused with exit 2. They move guests between Proxmox hosts only. |

A guest that is stopped can be started with `incus start <remote>:<name>`; commands that need a running guest say so.

## VMs

`province env create api --host box --kind vm` makes the env an Incus virtual machine (`type = "virtual-machine"`) from the same image alias; Incus picks the image's VM variant. A VM has its own kernel, so it is a stronger wall than a container: use it for untrusted code. It boots in 10 to 30 seconds; before the bootstrap, province waits (up to two minutes) until `incus exec` reaches the VM's agent.

- The server needs KVM (`/dev/kvm`); in a Proxmox VM, give it CPU type `host`.
- `--cpu`, `--ram` and `--disk` work as for containers; the root disk is created at `--disk` GiB.
- `--docker` installs Docker as usual; a VM needs none of the container `security.*` keys, so none are set.
- `tailscale.tun: device` is refused for a VM: it passes the host's `/dev/net/tun` into a container, and a VM has its own. Keep `tun: userspace` (the default).
- The kind is recorded with the env: a re-run of `env create` keeps it, and a different `--kind` is refused until the env is destroyed. Records from before kinds existed are containers.

## Limits

- `--kind vm` works on Incus only; on a Proxmox host it is refused (exit 2) until province can create VMs there (that needs a cloud-init template with qemu-guest-agent).
- An env stays on the backend it was created on. If a host's `backend` changes in the config, commands on its existing envs are refused; destroy and recreate them.
