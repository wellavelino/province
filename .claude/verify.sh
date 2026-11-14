#!/usr/bin/env bash
# Project verification gate, run by ~/.claude/hooks/verify.sh from the repo root.
# Must not write into the working tree: the caller fingerprints it before and after.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

step() { printf '== %s\n' "$*"; }

step "gofmt"
unformatted=$(gofmt -l .)
if [ -n "$unformatted" ]; then
  printf 'gofmt: these files need formatting:\n%s\n' "$unformatted" >&2
  exit 1
fi

step "go vet"
go vet ./...

step "go build"
go build ./...

step "go mod tidy -diff"
go mod tidy -diff

step "go test -race"
CGO_ENABLED=1 go test -race -count=1 ./...

step "shellcheck"
for f in internal/bootstrap/bootstrap.sh internal/bootstrap/recipe.sh scripts/tofu-validate.sh scripts/e2e-incus.sh .claude/verify.sh; do
  if [ -f "$f" ]; then
    shellcheck -s bash "$f"
  elif [ "$f" = internal/bootstrap/bootstrap.sh ] && [ -d internal/bootstrap ]; then
    echo "shellcheck: $f is missing" >&2
    exit 1
  fi
done

step "staticcheck"
if command -v staticcheck >/dev/null 2>&1; then
  staticcheck ./...
else
  echo "staticcheck: not on PATH, skipped"
fi

step "govulncheck"
if command -v govulncheck >/dev/null 2>&1; then
  govulncheck ./...
else
  echo "govulncheck: not on PATH, skipped"
fi

echo "verify: all checks passed"
