# memoryd

Personal Memory Vault. The current vertical slice durably imports and restores PDF, JPEG, and PNG Blobs through an OpenAPI-first Go server and CLI.

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
