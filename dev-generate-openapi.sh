#!/usr/bin/env bash

set -euo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)

echo "Generating server OpenAPI bindings..."
(cd "$script_dir/server" && go generate ./api)

echo "Generating CLI OpenAPI bindings..."
(cd "$script_dir/cli" && go generate ./internal/api)
