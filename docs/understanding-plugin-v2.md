# Understanding plugin protocol v2

Status: implemented. This specifies the process seam; it does not prescribe how a plugin extracts, describes, or analyzes source content.

## Configuration and selection

Configure trusted local executables explicitly; there is no discovery or manifest:

```yaml
understanding:
  max_concurrent: 1
  plugins:
    pdf:
      media_types: [application/pdf]
      command: [/absolute/path/to/pdf-understand, --mode, strict]
      log_dir: ./data/understanding-logs
```

The `plugins` map key is the stable plugin ID associated with attempts and artifacts. `command` is an argument vector launched directly, never a shell command. Absolute paths are supported; relative paths containing `/` resolve from the server's working directory; bare executable names resolve through `PATH`. Each plugin declares one or more exact input Blob Media Types, and a Media Type may belong to only one plugin. `max_concurrent` defaults to `1` and limits workers across imports and explicit Rebuilds.

Memoryd validates configuration at startup but does not launch or probe plugin executables. A missing executable fails its first attempt and is recorded. A Blob with no matching plugin receives a successful Run with a warning naming its unsupported input Media Type. Adding a plugin later does not automatically rebuild earlier Runs.

Optional per-plugin `log_dir` saves exact process output after exit as `<log_dir>/<understanding_run_id>.stdout.log` and `.stderr.log`, including empty streams and failed or interrupted executions. An omitted or empty value disables file logging. Relative paths use the server's working directory; directories are created with mode `0700`, files with mode `0600`. Treat captures as private; they may contain source content.

Model settings are centrally configured as `models.document_understanding` and passed to the plugin in the request. Model-using plugins use those supplied settings; they must not select a fallback model or provider. Model-free plugins need no model or cost reporting and may omit the request's `model`. Codex uses the server user's local sign-in; credentials and extra app-server arguments are not part of the request. The Codex extractor requires its configured model. The standalone diagnostic CLI omits `model` unless all model flags are supplied.

## Process lifetime and transport

Memoryd launches one child process per understanding attempt. A global `max_concurrent` limit applies across imports and explicit Rebuilds. There is no resident daemon, plugin server, or plugin-to-plugin call. Shutdown cancels active children; restart schedules nothing, so interrupted attempts require an explicit Rebuild.

Memoryd sends one UTF-8 JSON request on standard input, then closes it. The request embeds Blob bytes as base64; plugins receive no Vault path and may write their own temporary file if needed. Standard output contains exactly one UTF-8 JSON result. Standard error is reserved for diagnostics and is never parsed as a result. Plugins may emit standalone `MEMORYD_PROGRESS ` JSON lines to stderr; these do not replace the final response.

Requests use `protocol_version: 2`; optional fields may be omitted:

```json
{
  "protocol_version": 2,
  "blob": {
    "blobref": "sha256-...",
    "media_type": "application/pdf",
    "byte_size": 12345,
    "content_base64": "JVBERi0xLjQK..."
  },
  "import_context": { "original_filename": "bill.pdf" },
  "user_note": "Focus on payment dates; present the summary in Russian.",
  "model": {
    "provider": "codex_app_server",
    "name": "gpt-6-luna",
    "reasoning_effort": "medium",
    "command": ["/absolute/path/to/codex", "app-server", "--stdio"]
  }
}
```

`model` may be omitted for model-free plugins. Memoryd supplies `import_context`, including `original_filename`; path and timestamp fields are optional. `blob.media_type` describes the input Blob and remains distinct from an artifact's `content_type`.

`user_note` is optional owner guidance, separate from immutable Import Context and document evidence. Memoryd sends the exact saved string when nonempty and omits it when empty. Plugins may ignore it. The Codex extractor requires a string and adds JSON-quoted guidance to both model turns, bounded by the required headings, output schema, source-grounding rules, and document-only access. Notes may guide language or presentation; these boundaries are best effort, not a prompt-injection guarantee. Private prompt and process logs may contain notes.

## Response contract

The response object requires only integer `protocol_version: 2` and an `artifacts` array. Each artifact requires a nonempty string `content_type` and string `content`. Empty artifact arrays and empty content strings are valid. Additional response and artifact fields are allowed. There is no artifact-kind taxonomy or content-type allowlist. The exact response shape is defined by the shared [protocol v2 JSON Schema](../server/internal/understanding/plugin-response-v2.schema.json).

`content` is always literal text. A `content_type` such as `application/json` does not make memoryd parse or validate embedded JSON. Optional `provenance` and `scope` are opaque JSON values retained as supplied; their shape is plugin-defined.

Example response with two text artifacts and optional reporting:

```json
{
  "protocol_version": 2,
  "plugin_version": "1.0.0",
  "warnings": [],
  "artifacts": [
    {
      "content_type": "text/markdown",
      "content": "# Account statement\n...",
      "provenance": { "method": "PDF text extraction", "tool_version": "1.0" },
      "scope": { "page": 1 }
    },
    {
      "content_type": "application/json",
      "content": "{\"document_type\":\"account_statement\"}",
      "provenance": { "method": "field extraction" }
    }
  ],
  "statistics": {
    "usage": {
      "input_tokens": 31000,
      "cached_input_tokens": 20000,
      "cache_write_input_tokens": 0,
      "output_tokens": 638,
      "reasoning_output_tokens": 0,
      "total_tokens": 31638
    }
  },
  "cost_estimate": {
    "amount_usd": 0.001619,
    "basis": "standard_api_equivalent_short_context",
    "pricing_date": "2026-09-30",
    "pricing_url": "https://developers.openai.com/api/docs/models/gpt-6-luna"
  }
}
```

`statistics` is optional and closed: it permits only optional `usage`. `usage` is optional and closed, with only these optional nonnegative integer counts: `input_tokens`, `cached_input_tokens`, `cache_write_input_tokens`, `output_tokens`, `reasoning_output_tokens`, and `total_tokens`. `cost_estimate` is optional and closed; it requires nonnegative numeric `amount_usd` and nonempty string `basis`, and permits optional string `pricing_date` and `pricing_url`. The schema does not require any usage count.

Memoryd exposes `statistics` and `cost_estimate` directly as optional Run fields in API responses and CLI reports/captures; they are not nested in an opaque reporting envelope or copied to artifacts. Parseable `plugin_version` and `warnings` populate their dedicated Run fields. Additional unrecognized response properties are ignored, not retained as opaque reporting data. Reporting describes the plugin response as a whole, not a per-artifact charge. The Codex extractor reports cumulative usage only; per-turn accounting remains in its stderr logs.

## Progress and outcomes

Plugins may emit standalone stderr lines prefixed with `MEMORYD_PROGRESS ` followed by compact JSON, for example:

```text
MEMORYD_PROGRESS {"phase":"model_started","turn":1}
```

Recognized phases are `request_validated`, `source_staged`, `model_runtime_ready`, `model_started`, `model_completed`, `result_validated`, and `completed`. Optional `turn` is a positive integer. Emit progress at workflow boundaries, not per token or tool event, and keep it separate from diagnostic text. Plugins that omit progress records still work; malformed or unknown records do not interrupt stderr draining or change the result.

A zero exit plus one schema-valid v2 response completes the plugin attempt. Nonzero exit, malformed output, or a missing response is an operational failure; memoryd does not activate a partial Run. Failed or interrupted attempts require an explicit Rebuild. Runs and active-Run selection commit atomically after derived Blob publication.

The web UI's split Rebuild control can submit a multiline note. Accepted Rebuilds save that preference on the Memory before RAM queue admission and capture it for the job. Main Rebuild reuses it. The optional JSON body on `POST /api/v0/memories/{memoryId}/rebuild` accepts `user_note`: no body, `{}`, or `null` reuses the preference; a string replaces it verbatim; `""` clears it. Non-string values or malformed JSON, including trailing data or multiple JSON values, return HTTP 400 with plain-text errors before saving a note or admitting work. A queued/running conflict returns the existing handle without saving the rejected note.

The right-hand SVG chevron is centered in a fixed-width segment; both split segments stay level on hover. It opens the note form, prefilled from the saved preference. Submit Rebuild inside that form to save edits and rebuild. Closing the form does not save. The main Rebuild segment reuses the saved note without opening the form.

Understanding details expose `user_note` as the latest saved preference. Each successful `active_run.user_note` records the exact note used, atomically with Run publication, independently of plugin output. Empty and legacy notes are `""`. Failure keeps the saved preference but preserves the previous successful Run and its note. Preferences and Run snapshots survive restart; startup schedules no work. A crash after saving can retain the preference while losing the in-memory Rebuild.

In the Understanding tab, the right sidebar shows the active Run's completion time above section navigation, using the same label/value styling as Import Context. With no successful Run, it shows `Not available`. Selecting Saved user note opens its preview using the same heading and plain-text layout as text artifacts, without note-specific card styling. The latest preference remains separate from the historical Run note in Run details.

Sidebar navigation lists Artefacts first, followed by Info containing Saved user note, Run details, and available Diagnostics. The Artefacts section is hidden when the active Run has no artifacts. Saved user note is hidden when the saved preference is empty; clearing a selected note switches the preview to the next available section.

Protocol v1 plugins are incompatible and must be updated to v2. Incompatible existing Vault schemas are rejected explicitly; memoryd does not migrate or reprocess them automatically.

## Local introspection command

Build `mem-understand` as a standalone diagnostic command under `server/cmd/`. It uses the same request builder, Media Type resolution, protocol validation, and child-process runner as production understanding; it does not require a running server or write to the Vault.

```sh
mem-understand /path/to/file.pdf --plugin=/absolute/path/to/pdf-plugin
```

To provide model settings, supply all model options together:

```sh
mem-understand /path/to/file.pdf \
  --plugin=/absolute/path/to/pdf-plugin \
  --model-provider=codex_app_server \
  --model-name=gpt-6-luna \
  --model-effort=medium \
  --app-server-command=/absolute/path/to/codex
```

The CLI derives Blob hash, size, input Media Type, and Import Context from the local file and runs one attempt without scheduling or concurrency management. It prints generic optional reporting and each artifact's content type, provenance, and literal content. With `--raw-dir`, it saves exact request JSON, stdout, stderr, execution metadata, and the readable report; otherwise it uses a private temporary directory. Captures include base64 Blob bytes and may contain private source data.
