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

Buttons, Original / Understanding tabs, and Memory list items highlight on hover
without moving or changing size.

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
launched without a shell. `max_concurrent` bounds imports and manual Rebuilds;
the default is one. Executables are not probed at startup.
Relative plugin paths containing `/` resolve from the server's working directory,
not the configuration file's directory. Bare executable names are resolved through
`PATH`.

Model-assisted plugins receive `models.document_understanding` from central
server configuration. The example config supplies the Codex extractor with
`provider`, `name`, `reasoning_effort`, and the Codex app-server command.
Omit this section for model-free plugins; the Codex extractor requires it.
Codex uses the server user's existing local sign-in, not credentials in YAML.
Codex executable paths follow the same relative-path and `PATH` rules as plugins.

At `info` level, logs show configuration counts, queueing, attempt and plugin
start/exit, live plugin phases, completion, warnings, and optional plugin
reporting. Raw plugin stderr is logged only when the plugin fails, never on
success even at `debug`. Treat failure logs as private: plugin diagnostics may
contain source content. Plugins can report concise structured progress records;
their model responses and other diagnostic text are not forwarded.
Background execution retains the initiating HTTP `request_id`, independent of
client cancellation. Polls and rejected duplicate requests have their own IDs.

Set `log_dir` per plugin to save raw output after each process exits as
`<log_dir>/<understanding_run_id>.stdout.log` and
`<log_dir>/<understanding_run_id>.stderr.log`. Omit it or leave it empty to disable
file logging. Relative paths resolve from the server's working directory.
Missing directories are created with mode `0700`; files use mode `0600`.
Files include failed and interrupted process output. The Run ID is reserved
before execution and appears as `run_id` in the attempt-start log; only successful
processing records that Run in the Vault. File-write failures are logged without
changing the understanding outcome. These files may contain private source content.

`GET /api/v0/memories/{id}` includes `understanding.status` (`not_started`,
`queued`, `running`, `done`, or `failed`), `latest_attempt`, and `active_run`. Each active artifact
includes string `content`, `content_type`, derived Blobref, independent provenance,
and optional scope. Optional `statistics` and `cost_estimate` are direct Run
fields, not copied to artifacts. Statistics may contain cumulative token counts;
cost estimates include amount and basis, with optional pricing details.
Unrecognized top-level response fields are not retained. See the
[protocol v2 contract](docs/understanding-plugin-v2.md) for exact constraints.
Unsupported formats complete with a warning-only Run. Failed attempts retain diagnostics without replacing an earlier active Run.
Scheduling is process-local and best effort. Restart drops queued/running work;
startup schedules nothing. Successful Runs and terminal failure history remain
durable. Legacy pending SQLite rows are ignored. An import whose admission fails
remains a valid Memory requiring an explicit Rebuild.

`POST /api/v0/memories/{memoryId}/rebuild` requests a manual Rebuild using the
currently configured plugin. New work returns `202`; competing queued/running work
returns `409` without creating another attempt. Both return `{memory_id, attempt,
status_url}`. Poll `GET /api/v0/rebuild-status/{rebuildId}`, where `rebuildId` is
`attempt.id`, for that exact attempt and terminal diagnostics. The returned
`status_url` contains this path. Handles remain until a newer attempt is accepted,
Memory deletion, or restart; invalid handles return `404`. Responses use
`Cache-Control: no-store`. Durable history has no polling URL after restart.
The original Blob and prior active Run remain intact; only a successful new Run
replaces the active result. Missing Memories return `404`; an unavailable or
stopped worker returns `503`. Model-backed plugins may incur cost.

In desktop Memory details, **Original / Understanding** buttons switch between
the preserved preview and Understanding. A right-hand **Sections** sidebar selects
individual artifacts, **Run details**, or failure **Diagnostics** for the main area.
The first artifact is selected initially; refresh preserves the selection for the
same Run. Each artifact retains expandable **Metadata**; Markdown also provides
**View source**. Status and **Rebuild** share the header with **Download** and
**Delete**. Status includes **Understanding not started**. Clicking **Rebuild**
starts a new attempt; pending work disables the button. Automatic polling follows
the returned handle every three seconds while details are visible. Completion
reloads Memory details for the new Run; failure keeps the prior Run. An expired
handle reloads durable state without starting work. Failed detail reloads retry
while visible. These reads preserve Original preview and same-Run selection.

Markdown artifacts render headings, lists, and tables with exact **View source**
access. Raw HTML and unsafe links are disabled; all images become alt text without
automatic loads. Other content types remain literal text. Artifact **Metadata**,
**Run details**, and failure **Diagnostics** display formatted JSON. Failed
attempts preserve any last successful result; empty Runs show **No Derived Content**.
Run warnings appear in an amber **Understanding warnings** notice above the
content. Unsupported formats still complete with API status `done`.
The CLI `mem info` JSON output is unchanged.

Derived Blobs are stored in the separate `derived/sha256` namespace under the
configured Blob directory. SQLite records attempts, Runs, and active selection;
all artifact references and activation commit together after Blob publication.
Incompatible older Vault schemas are rejected explicitly; there are no automatic
schema migrations. Preserve existing data before separately upgrading storage
or rebuilding a Vault.

For standalone extraction without a server or Vault, use `mem-understand`.
The [Codex extractor](plugins/codex-extractor/README.md) accepts PDFs, PNGs, and
JPEGs with a locally authenticated Codex model, either through this command or
configured server plugins.

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
