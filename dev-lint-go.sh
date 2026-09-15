#!/usr/bin/env bash

set -euo pipefail

if ! command -v golangci-lint &>/dev/null; then
  echo "golangci-lint is unavailable on PATH" >&2
  exit 1
fi

topdir=$(git rev-parse --show-toplevel)
cd "$topdir"

can_write_cache() {
  local dir=$1
  local probe

  # Try a real write; directory permissions alone may not reflect sandbox restrictions.
  mkdir -p "$dir" 2>/dev/null || return 1
  probe=$(mktemp "$dir/.memoryd-cache-check.XXXXXX" 2>/dev/null) || return 1
  rm -f "$probe"
}

temporary_cache_dir=""
ensure_temporary_cache() {
  if [[ -z "$temporary_cache_dir" ]]; then
    temporary_cache_dir=$(mktemp -d "${TMPDIR:-/tmp}/memoryd-lint.XXXXXX")
    trap 'rm -rf "$temporary_cache_dir"' EXIT
    echo "Using temporary caches because the configured cache paths are not writable."
    echo "Temporary cache dir: $temporary_cache_dir"
  fi
}

go_cache_dir=$(go env GOCACHE)
if ! can_write_cache "$go_cache_dir"; then
  ensure_temporary_cache
  export GOCACHE="$temporary_cache_dir/go-build"
fi

lint_cache_dir=$(golangci-lint cache status 2>/dev/null | sed -n 's/^Dir: //p') || lint_cache_dir=""
if [[ -z "$lint_cache_dir" ]] || ! can_write_cache "$lint_cache_dir"; then
  ensure_temporary_cache
  export GOLANGCI_LINT_CACHE="$temporary_cache_dir/golangci-lint"
fi

echo "Formatting Go files..."
for module in cli server; do
  (cd "$module" && golangci-lint fmt ./...)
done

echo "Linting Go files..."
for module in server cli; do
  (cd "$module" && golangci-lint run ./...)
done
