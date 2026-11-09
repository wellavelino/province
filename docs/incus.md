# Incus hosts

Province can run environments on [Incus](https://linuxcontainers.org/incus/) as well as on Proxmox VE. Incus runs the same kind of LXC containers on any Linux machine, so a spare box or your own workstation is enough.

On an Incus host, an env is one Incus container. OpenTofu declares it (provider `lxc/incus`), and province runs the bootstrap and recipes through `incus exec`. Everything inside the guest is the same as on Proxmox: your user and SSH keys, sudo, optional Docker, Tailscale, [recipes](recipes.md) and Funnel.

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
| `tailscale.tun: device` | Yes: passes `/dev/net/tun` through as a `unix-char` device |
| `guest move`, `guest prune` | No: refused with exit 2. They move guests between Proxmox hosts only. |

A guest that is stopped can be started with `incus start <remote>:<name>`; commands that need a running guest say so.

## Limits

- Containers only; `--kind vm` is refused as on Proxmox.
- An env stays on the backend it was created on. If a host's `backend` changes in the config, commands on its existing envs are refused; destroy and recreate them.
