# Recipes

A recipe says what an environment should have inside: apt packages, language tools at pinned versions, your dotfiles. You write a short list in the site config; province turns it into a script, runs it in the guest after the bootstrap, and can run it again later with `env provision`. You don't write shell unless you want to.

Recipes work the same on Proxmox and Incus hosts: province only needs to run a script in the guest, which both backends can do.

## A first recipe

In your site config (`province.yaml`, in your own private repo):

```yaml
recipes:
  base:
    packages: [git, tmux, ripgrep, jq]
    dotfiles: https://github.com/you/dotfiles

  go:
    include: [base]
    tools: { go: "1.24", golangci-lint: latest }

  web:
    include: [base]
    packages: [postgresql]
    tools: { node: "22", pnpm: latest }

defaults:
  recipe: base          # used when --recipe is not given
```

Then:

```
province env create api --recipe go            # the plan lists what the recipe installs
province env create api --recipe go --apply    # create, bootstrap, then apply the recipe
```

The plan output shows the resolved recipe before anything is created:

```
recipe go
  packages  git tmux ripgrep jq
  tools     go@1.24 golangci-lint@latest
  dotfiles  https://github.com/you/dotfiles
```

## The keys

A recipe has five keys, all optional. They are applied in this order:

| Key | What happens in the guest |
|---|---|
| `include` | The named recipes are applied first, in order, then this one. |
| `packages` | `apt-get install -y` for the packages not installed yet. A name may carry a version: `postgresql-16=16.4-1`. |
| `tools` | [mise](https://mise.jdx.dev) is installed once (`/usr/local/bin/mise`), then `mise use --global name@version` runs as your user. Any tool mise knows works: `go`, `node`, `python`, `rust`, `terraform`, `kubectl`, and backends such as `npm:prettier` or `aqua:cli/cli`. Versions are exact (`"1.24.3"`), a prefix (`"1.24"`), `lts` or `latest`. |
| `dotfiles` | An https git URL, cloned as your user into `~/.dotfiles` (pulled if it is already there). If the repo has an `install.sh`, it runs as your user from that directory. |
| `run` | Commands run as root with `bash -euo pipefail -c`, after everything above. `P_USER` and `P_HOSTNAME` are set. This is the escape hatch for anything the other keys don't cover. |

When recipes are merged through `include`:

- `packages` and `run` accumulate in order; a package listed twice is installed once.
- `tools` merge by name; the later recipe's version wins.
- `dotfiles` from the later recipe wins.
- A recipe included twice (directly and through another) is applied once.

Province checks recipes when it loads the config, before it touches any host: unknown keys, unknown includes, include loops, package and tool names that are not valid, versions with spaces or shell characters, and non-https dotfiles URLs are all refused (exit 2).

## Re-applying a recipe

Every step is safe to run twice, so you can edit a recipe and apply it again:

```
province env provision api                 # the env's recorded recipe
province env provision api --recipe web    # switch the env to another recipe
```

The env must be bootstrapped and running. The recipe chosen is recorded in the env's `province.json` (`recipe`), and `env list` / `env show` show it.

Re-applying adds what is missing. It does not remove packages or tools you dropped from a recipe; recreate the env for a clean slate.

## Choosing a recipe

`env create` and `env provision` pick the recipe in this order:

1. `--recipe <name>`
2. the recipe recorded for the env (so a plan's choice carries over to `--apply`)
3. `defaults.recipe`

`--recipe none` (or `defaults.recipe: none`) applies no recipe.

## How the tools reach your shell

mise puts a shim for each tool in `~/.local/share/mise/shims`. The recipe adds that directory to `PATH` on the first line of `~/.bashrc`, before Debian's "not interactive" early return, so both an interactive login and a one-off command over SSH find them:

```
ssh dev@api 'cd ~/src/api && go test ./...'
```

A project can also pin its own versions in a `.mise.toml` next to its code; mise uses it automatically when you work in that directory:

```toml
[tools]
go = "1.24"
node = "22"
```

## A database per env

Each env is its own container, so a database a recipe installs belongs to that env alone: one per branch, gone on `env destroy`. This recipe installs PostgreSQL, makes your user a database role, and creates an empty `app` database:

```yaml
recipes:
  web:
    include: [base]
    packages: [postgresql]
    tools: { node: "22", pnpm: latest }
    run:
      - systemctl enable --now postgresql
      - sudo -u postgres psql -tc "select 1 from pg_roles where rolname='$P_USER'" | grep -q 1 || sudo -u postgres createuser -s "$P_USER"
      - sudo -u postgres psql -tc "select 1 from pg_database where datname='app'" | grep -q 1 || sudo -u postgres createdb -O "$P_USER" app
```

Each `run` line checks before it creates, so `env provision` can apply it again.

Connect over the local socket as your user; no password is needed:

```
ssh dev@api 'psql postgresql:///app'
DATABASE_URL=postgresql:///app
```

`postgresql://localhost/app` asks for a password, because Debian's PostgreSQL uses password auth over TCP. Use the socket URL; recipes must not carry passwords.

The recipe gives you an empty database. Schema and seed data come from the project's own migrations, run in the env (`pnpm db:migrate`, for example), since province never runs setup from a project repo. Nothing copies production data into an env.

## Using other tools from a recipe

Province doesn't need to know about the tool you prefer. Install it with `packages` or `run`, then call it from `run`.

**Ansible**, run inside the guest with `ansible-pull`, so neither province nor your machine needs Ansible:

```yaml
recipes:
  ops:
    include: [base]
    packages: [ansible-core]
    run:
      - ansible-pull -U https://github.com/you/playbooks dev.yml
```

**Nix**, for a project that ships a `flake.nix` (the Nix store takes a few GiB, so give the env more disk):

```yaml
recipes:
  nix:
    include: [base]
    packages: [nix-setup-systemd]
    run:
      - usermod -aG nix-users "$P_USER"
      - mkdir -p /etc/nix && grep -qx 'experimental-features = nix-command flakes' /etc/nix/nix.conf || echo 'experimental-features = nix-command flakes' >> /etc/nix/nix.conf
      - systemctl enable --now nix-daemon
```

**A script of your own**, kept in your dotfiles or another repo:

```yaml
recipes:
  custom:
    dotfiles: https://github.com/you/dotfiles
    run:
      - sudo -u "$P_USER" -H bash ~/.dotfiles/setup-extra.sh
```

Whatever you put in `run`, keep it safe to repeat: `env provision` runs the whole list again.

## Secrets and private repos

Recipes go into the guest as a plain script. Don't put tokens or passwords in them. `dotfiles` must be an https URL without credentials; a private dotfiles repo is not supported yet.

Recipes only come from your own site config. Province never runs setup from a project repo it clones.

## When a recipe fails

During `env create --apply`, a failing recipe does not undo the env: it is bootstrapped and on the tailnet. The command exits 1, names the step that failed, and tells you to fix the recipe and run `province env provision <name>`.

The output of each step goes to stderr with a `province-recipe:` prefix, and a failing `run` command stops the list at that command.
