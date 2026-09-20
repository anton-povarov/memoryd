# memoryd

Personal Memory Vault. The current vertical slice durably imports and restores any Blob up to 100 MiB through an OpenAPI-first Go server and CLI.

The implemented slice supports import, browse, detail, and download. It ends a
successful import after the Blob is durably published and its Memory is
committed. Document Understanding, search, processing history, and Rebuild are
not exposed yet; `docs/MVP.md` describes the target product rather than the
current implementation.

The development database has no migration path yet. If its schema is
incompatible, recreate the SQLite database and reimport from the original
content.

The server detects a Blob's Media Type from its content. When detection yields only generic binary or plain text, a specific multipart part `Content-Type` supplies the type; unknown content remains importable. The CLI supplies a type based on the filename extension, including `text/markdown` for `.md` and `.markdown` files.

The language-neutral contract is `api/openapi.yaml`. The `server/` and `cli/` directories are independent Go modules with their own generated bindings; future web and integration clients can remain independent siblings too.

## Run

```sh
cd server
go run ./cmd/memoryd
```

Use an explicit configuration file when needed:

```sh
cd server
go run ./cmd/memoryd -c memoryd.example.yaml
```

The server binds to `http://127.0.0.1:8080/` by default.

Open <http://127.0.0.1:8080/> for the browser interface. It supports importing,
browsing, previewing, inspecting, and downloading the currently implemented
Memory data.

Import, inspect, and restore one Memory from another terminal:

```sh
cd cli
go run ./cmd/mem put /absolute/path/to/memory.pdf
go run ./cmd/mem info <memory-id>
go run ./cmd/mem get <memory-id>
go run ./cmd/mem list -n 20
```

Use `mem [--server ADDRESS] (get|put|info|list) [subcommand options]`. Set global `--server` before the subcommand; otherwise `mem` uses `MEMORYD_URL`, then `http://127.0.0.1:8080`. `mem info` prints Memory details as JSON. `mem get` accepts `-o <path>`, `-o -` for stdout, and `--force` when replacing an existing destination. `mem list` returns 50 Memory summaries by default; `-n N` sets a total across pages, `--all` returns every page, and `--short` prints one Memory ID per line. `-n` and `--all` cannot be combined.

For compact development logs, set `MEMORYD_DEV` to a true Boolean value:

```sh
MEMORYD_DEV=1 go run ./cmd/memoryd
```

Development formatting applies to terminal and redirected output. Attributes follow the human-readable prefix as an indented JSON object. When development mode is disabled, logs use standard structured JSON.

- Interactive API documentation: <http://127.0.0.1:8080/docs/>
- OpenAPI YAML: <http://127.0.0.1:8080/openapi.yaml>
- OpenAPI JSON: <http://127.0.0.1:8080/openapi.json>
- API base: <http://127.0.0.1:8080/api/v0>

## Develop

Enable the repository's pre-commit hook in this clone:

```sh
git config core.hooksPath .githooks
```

The hook runs `dev-lint-go.sh` to format and lint all Go packages in both modules.
It blocks commits when the script fails, including when `golangci-lint` is missing.

Regenerate each module's transport code after changing the root contract:

```sh
(cd server && go generate ./...)
(cd cli && go generate ./...)
```

Verify both modules independently:

```sh
(cd server && go test ./... && go vet ./...)
(cd cli && go test ./... && go vet ./...)
```
