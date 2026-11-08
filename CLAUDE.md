# province: agent instructions

- Commit and push directly to `main`; no feature branches or pull requests in this repo (owner decision, 2026-11-08). This overrides the global "feature branch + PR" rule for this repo only.
- The repo is private now and will go public: never commit site details (IPs, hostnames, device lists, emails) or secrets. Site config and secrets live in each user's own private repo.
- Run `bash ~/.claude/hooks/verify.sh` before pushing; it runs `.claude/verify.sh`.
- Never run `province` against real Proxmox hosts from an agent session unless the owner asks for that run: one of the hosts carries production.
