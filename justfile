# adaptive-power-manager dev / CI tasks.

# Mirrors the CI gates (golangci-lint, go vet/test/build + nix build
# vendorHash) so a green `just check` predicts green CI.
default:
    @just --list

check: lint vet test build

lint:
    golangci-lint run

# Autofix what the linters can (modernize, errcheck, gocritic, formatters).
lint-fix:
    golangci-lint run --fix

vet:
    go vet ./...

test:
    go test ./...

build:
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /dev/null .

fmt:
    gofmt -w .

# Recompute the Go module vendorHash in flake.nix from go.mod/go.sum, the
# same way .github/workflows/flake.yml does. Run after any dependency change.
sync-flake:
    #!/usr/bin/env bash
    set -euo pipefail
    fake="sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
    cur="$(grep -oP 'vendorHash = "\K[^"]+' flake.nix)"
    sed -i "s#vendorHash = \"${cur}\"#vendorHash = \"${fake}\"#" flake.nix
    out="$(nix build .#default --no-link 2>&1 || true)"
    got="$(printf '%s' "$out" | grep -oP 'got:\s+\K(sha256-\S+)' | head -1 || true)"
    if [ -z "$got" ]; then
        got="$cur"
    fi
    sed -i "s#vendorHash = \"${fake}\"#vendorHash = \"${got}\"#" flake.nix
    # A build that fails without reporting a hash (wrong Go toolchain,
    # broken source) must fail loudly, not silently keep the old hash: the
    # CI build would then fail anyway, with a much more confusing error.
    if ! nix build .#default --no-link >/dev/null 2>/tmp/sync-flake.err; then
        echo "nix build fails for a reason other than a stale vendorHash:" >&2
        tail -20 /tmp/sync-flake.err >&2
        exit 1
    fi
    echo "vendorHash = ${got}"
