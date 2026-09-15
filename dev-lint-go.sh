#!/usr/bin/env bash

set -euo pipefail

if ! command -v golangci-lint &>/dev/null; then
  echo "golangci-lint is unavailable on PATH" >&2
  exit 1
fi

topdir=$(git rev-parse --show-toplevel)

cd "$topdir"

echo "Formatting Go files..."
for module in cli server; do
  (cd "$module" && golangci-lint fmt ./...)
done

echo "Linting Go files..."
for module in server cli; do
  (
    cd "$module"
    golangci-lint run ./...
  )
done
