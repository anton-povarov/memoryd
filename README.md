# MemoryD

Local, single-user Personal Memory Vault.

Stores content and medatata, understands it with tools and LLMs and provides natural language search.

Status: MVP in-progress, see [`docs/MVP.md`](docs/MVP.md).

The current implementation imports files up to 100 MiB and supports browsing,
inspection, and download through a web UI, CLI, and OpenAPI HTTP interface.
After the durable import commit, background Understanding Plugins can produce
separately stored Derived Content. Memory details expose attempt status,
diagnostics, and the last complete active Understanding Run.

## Setup

Requires Go 1.25 or newer.

```sh
go mod download
go run ./server/cmd/memoryd
```

Or with a config file: replace both executable paths in
`server/memoryd.example.yaml` and choose a model available to your signed-in
local Codex installation before importing documents.
```sh
go run ./server/cmd/memoryd -c server/memoryd.example.yaml
```

## Usage

Open <http://127.0.0.1:8080/> for the web UI.

```sh
go run ./cli/cmd/mem put /absolute/path/to/file
go run ./cli/cmd/mem list
go run ./cli/cmd/mem info <memory-id>
go run ./cli/cmd/mem get <memory-id>
go run ./cli/cmd/mem delete <memory-id>
```

Use `mem --server ADDRESS ...` or set `MEMORYD_URL` to connect to another
server. Run a command without its required arguments to see its usage.

- API: <http://127.0.0.1:8080/api/v0>
- API documentation: <http://127.0.0.1:8080/docs/>
- OpenAPI specification: [`api/openapi.yaml`](api/openapi.yaml)

### Document Understanding

Configure trusted plugin executables and exact Media Types in the `understanding`
section of the example server configuration. Commands are argument vectors
launched without a shell. `max_concurrent` bounds both new imports and recovered
attempts; the default is one. Executables are not probed at startup.

Model-assisted plugins receive `models.document_understanding` from central
server configuration. The example config supplies the Codex extractor with
`provider`, `name`, `reasoning_effort`, and the absolute Codex app-server command.
Omit this section for model-free plugins; the Codex extractor requires it.
Codex uses the server user's existing local sign-in, not credentials in YAML.

At `info` level, logs show configuration counts, queueing, attempt and plugin
start/exit, completion, warnings, and failures. Failures include captured plugin
stderr; `debug` also exposes stderr from successful plugins. Treat these logs as
private: plugin diagnostics may contain source content. Stderr is captured until
process exit, not streamed live.

`GET /api/v0/memories/{id}` includes `understanding.status` (`queued`, `running`,
`done`, or `failed`), `latest_attempt`, and `active_run`. Each active artifact
includes its content, derived Blobref, kind, Media Type, independent provenance,
and optional scope. Unsupported formats complete with a warning-only Run.
Failed attempts retain diagnostics without replacing an earlier active Run.
Interrupted and failed work receives one new attempt on the next server start;
completed Runs are not automatically rerun when configuration changes.

Derived Blobs are stored in the separate `derived/sha256` namespace under the
configured Blob directory. SQLite records attempts, Runs, and active selection;
all artifact references and activation commit together after Blob publication.
Older incompatible Vault schemas are rejected explicitly; there are no schema
migrations. Preserve existing data before recreating a Vault and reimporting.

For standalone extraction without a server or Vault, use `mem-understand`.
The [Codex extractor](plugins/codex-extractor/README.md) accepts PDFs, PNGs, and
JPEGs with a locally authenticated Codex model, either through this command or
configured server plugins. Client presentation of understanding remains separate
work.

## Development

```sh
./dev-generate-openapi.sh   # regenerate OpenAPI specification
./dev-lint-go.sh            # format and lint Go code
go test ./...               # run all tests
```

`dev-lint-go.sh` requires `golangci-lint` on `PATH`.

Enable the pre-commit hook for this clone:

```sh
git config core.hooksPath .githooks
```
