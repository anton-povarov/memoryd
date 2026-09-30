# Understanding plugin protocol v1

Status: implemented. This specifies the v1 process seam; it does not prescribe how a plugin extracts text, performs OCR, or generates a description.

## Scope

The first plugin handles PDFs and may return multiple pieces of Derived Content from one invocation. Initially useful kinds are `document_text` (source words with layout preserved where possible) and `description` (a generated account of the document). Both are searchable, stored separately, and retain their own provenance. Neither kind asserts a structured Fact. A plugin may return one, both, or neither, with warnings explaining limited coverage.

The `document_text` and `description` kinds use UTF-8 Markdown (`text/markdown`). A plain paragraph is valid Markdown for a description. For document text, the plugin should retain observable structure such as page boundaries, headings, lists, and tables where it can do so without inventing structure. The `document_data` kind uses `application/json`; its `content` is a serialized JSON object, not an array or scalar. This lets a plugin preserve structured data it can extract without claiming those values are verified Facts.

## Configuration and selection

Configure trusted local executables explicitly; there is no discovery or manifest:

```yaml
understanding:
  max_concurrent: 1
  plugins:
    pdf:
      media_types: [application/pdf]
      command: [/absolute/path/to/pdf-understand, --mode, strict]
```

The `plugins` map key is the nonempty, stable plugin ID associated with attempts and artifacts. `command` is an argument vector launched directly, never a shell command; its executable must be absolute. Each plugin declares one or more exact canonical Media Types, and a Media Type may belong to only one plugin. `max_concurrent` defaults to `1` and limits all workers across imports and startup recovery.

Memoryd validates configuration at startup but does not launch or probe plugin executables. A missing executable fails its first attempt and is recorded. A Blob with no matching plugin receives a successful Run with a warning naming its unsupported Media Type. Adding a plugin later does not automatically rebuild earlier Runs.

Server configuration optionally supplies `models.document_understanding`, passed as the request's `model` to the selected plugin. Its fields are `provider: codex_app_server`, `name`, `reasoning_effort`, and `command: [/absolute/path/to/codex, app-server, --stdio]`; see [`server/memoryd.example.yaml`](../server/memoryd.example.yaml). The Codex extractor requires this section; model-free plugins may omit it. Codex uses the server user's local sign-in. Credentials and extra app-server arguments are rejected. There is no automatic provider/model fallback.

## Process lifetime and transport

Memoryd launches one child process per understanding attempt. A global `max_concurrent` limit applies across imports and startup recovery. There is no resident daemon, plugin server, or plugin-to-plugin call. Shutdown cancels active children; interrupted attempts are eligible for retry on restart.

Memoryd sends exactly one UTF-8 JSON request on standard input, then closes it. The request embeds the Blob bytes as base64. Model settings are optional; the standalone diagnostic tool omits them when none are supplied. The plugin receives no Vault path and can write its own temporary file if a tool needs one. This adds about one third to the input byte count; for the initially small PDFs, the simpler, path-independent contract is worth that cost. The existing 100 MiB import limit still applies, so implementations should avoid unnecessary full-size copies when encoding and decoding. Standard output contains exactly one UTF-8 JSON result; standard error is reserved for diagnostics and never parsed as a result. Memoryd must consume both streams while the process runs and must not commit a partial result.

Example request (illustrative identifiers and abbreviated content):

```json
{
  "protocol_version": 1,
  "blob": {
    "blobref": "sha256-...",
    "media_type": "application/pdf",
    "byte_size": 12345,
    "content_base64": "JVBERi0xLjQK..."
  },
  "import_context": { "original_filename": "bill.pdf" },
  "model": {
    "provider": "codex_app_server",
    "name": "gpt-6-luna",
    "reasoning_effort": "medium",
    "command": ["/absolute/path/to/codex", "app-server", "--stdio"]
  }
}
```

The diagnostic CLI omits `model` unless all model flags are supplied. Its Codex
app-server options are `--model-provider=codex_app_server`, `--model-name=NAME`,
`--model-effort=minimal|low|medium|high|xhigh`, and
`--app-server-command=/absolute/path/to/codex`. The last option accepts only an
absolute executable path; the CLI appends `app-server --stdio`. Credentials are
read by the local Codex installation and are not included in the request.

Example result:

```json
{
  "protocol_version": 1,
  "plugin_version": "0.1.0",
  "usage": {
    "input_tokens": 31531,
    "cached_input_tokens": 23040,
    "cache_write_input_tokens": 0,
    "output_tokens": 107,
    "reasoning_output_tokens": 0,
    "total_tokens": 31638
  },
  "turn_usage": [
    {
      "turn": 1,
      "usage": {
        "input_tokens": 15531,
        "cached_input_tokens": 13000,
        "cache_write_input_tokens": 0,
        "output_tokens": 57,
        "reasoning_output_tokens": 0,
        "total_tokens": 15588
      }
    },
    {
      "turn": 2,
      "usage": {
        "input_tokens": 16000,
        "cached_input_tokens": 10040,
        "cache_write_input_tokens": 0,
        "output_tokens": 50,
        "reasoning_output_tokens": 0,
        "total_tokens": 16050
      }
    }
  ],
  "cost_estimate": {
    "amount_usd": 0.001133789,
    "basis": "standard_api_equivalent_short_context",
    "pricing_date": "2026-09-28",
    "pricing_url": "https://example.com/pricing"
  },
  "artifacts": [
    {
      "kind": "document_text",
      "media_type": "text/markdown",
      "content": "# Account statement\n...",
      "provenance": { "method": "pdf extraction", "tool": "example-tool", "tool_version": "1.0" },
      "scope": { "page": 1 }
    },
    {
      "kind": "description",
      "media_type": "text/markdown",
      "content": "An account statement for a billing period.",
      "provenance": { "method": "model description", "model": "example-model" }
    },
    {
      "kind": "document_data",
      "media_type": "application/json",
      "content": "{\"document_type\":\"account_statement\",\"currency\":\"USD\"}",
      "provenance": { "method": "field extraction", "tool": "example-tool" }
    }
  ],
  "warnings": []
}
```

`usage` and `cost_estimate` are optional top-level fields for backward
compatibility. If present, `usage` contains nonnegative integer
`input_tokens`, `cached_input_tokens`, `output_tokens`,
`reasoning_output_tokens`, and `total_tokens`; `cache_write_input_tokens` is
optional. `cost_estimate` contains a nonnegative numeric `amount_usd`, a
nonempty `basis`, a valid `pricing_date` in `YYYY-MM-DD` form, and a nonempty
`pricing_url`. Plugins calculate estimates from their own pricing source; Go
does not encode model prices. A basis such as
`standard_api_equivalent_short_context` identifies an API-equivalent estimate.
The diagnostic report prints the basis, price source, and date, and labels this
as an API-equivalent estimate rather than an actual subscription charge.

`turn_usage` is an optional array of objects with a unique positive integer
`turn` and a `usage` object with the same shape as top-level `usage`. The
codex-description plugin reports its two model calls as turns 1 and 2. Top-level
`usage` remains the cumulative total. When both top-level `usage` and a
nonempty `turn_usage` array are present, each required token count in the turn
entries must sum to the corresponding cumulative count. If cumulative usage
includes `cache_write_input_tokens`, every turn must report that field and its
counts must sum to the cumulative cache-write count. When cumulative usage
omits that optional field, cache-write counts are not compared. An empty
`turn_usage` array contains no breakdown and does not undergo sum validation.

Each artifact has its own kind, representation, content, and provenance. An optional `scope` object carries plugin-defined page or region references and is retained as supplied. The configured plugin ID, reported plugin version, Blob identity, and attempt ID are also recorded for each stored artifact by memoryd. A plugin may produce multiple artifacts of the same kind, for example one per page.

The result is one complete response, not a progress stream. The UI can initially show queued, running, done, or failed. The protocol can gain progress events only if actual processing times make them useful.

## Local introspection command

Build `mem-understand` as a standalone diagnostic command under `server/cmd/`. It does not require a running server or write to the Vault. It must use the same request builder, Media Type resolution, protocol validation, and child-process runner as production understanding, so its wire exchange is representative of a real attempt.

```bash
mem-understand /path/to/file.pdf --plugin=/absolute/path/to/pdf-plugin
```

To provide a model selection to the plugin, supply all model options together:

```bash
mem-understand /path/to/file.pdf \
  --plugin=/absolute/path/to/pdf-plugin \
  --model-provider=codex_app_server \
  --model-name=gpt-6-luna \
  --model-effort=medium \
  --app-server-command=/absolute/path/to/codex
```

The command accepts an absolute executable path through `--plugin` and does not resolve server-configured plugin IDs or read server configuration. The command derives Blob hash, size, Media Type, and Import Context from the local file using the import path's rules. The local path may appear as Import Context but is not how the plugin reads the Blob. It runs one attempt without scheduling or concurrency management.

The command prints the result as a readable report: selected plugin, input metadata, exit or protocol error, optional per-turn and cumulative token usage, cost estimate, warnings, then each artifact's kind, provenance, and content under a Markdown or JSON heading. JSON content is shown in a JSON code fence. An API-equivalent cost estimate is clearly identified as an estimate and not an actual subscription charge. If `--raw-dir` is supplied, captures are written there and may replace earlier files. Otherwise the command creates a private temporary directory. It reports the capture location and saves the exact request JSON (including base64 Blob bytes), exact stdout even when invalid, stderr, execution metadata, and the readable report on success or failure. Each returned artifact is also saved as its exact content under `derived-content/`, with a `.md` extension for Markdown and `.json` for JSON; paths appear in the report and execution metadata. The command exits nonzero when execution or protocol validation fails.

## Outcomes and durability

- Exit zero plus one valid v1 result means the plugin completed. Empty artifacts and warnings are valid for an opaque or poorly understood PDF.
- Nonzero exit, malformed output, or a missing result is an operational failure. Memoryd records diagnostics and does not activate a partial Run. A failed attempt is eligible for one new attempt on the next server start, so fixing a missing executable does not require reimport; it is not retried in a tight loop.
- Memoryd stores each returned artifact's bytes as an immutable, content-addressed derived Blob under a path separate from imported Blobs, for example `derived/sha256/...`. SQLite records its Blob reference, kind, media type, provenance, and Run membership. Identical bytes can share storage while retaining separate provenance rows.
- Memoryd stages and publishes derived Blobs, then commits the immutable Understanding Run, artifact references, and active-Run selection in one SQLite transaction. A crash after publication but before that transaction may leave an orphan derived Blob, as with import. Search indexes are projections of the committed active Run.
- SQLite, rather than an in-memory queue, records attempts. On import commit and startup, memoryd finds Memories needing an attempt. An attempt interrupted by shutdown is retried; a committed Run is not silently rerun.

The Vault persists attempts, derived-content references, immutable Runs, and active-Run selection in SQLite. Import responses end after Memory commit while detached workers process queued attempts; startup recovery requeues interrupted or failed work once, with no in-session retries.
