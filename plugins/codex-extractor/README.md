# Codex document extractor

This Understanding Plugin accepts a v1 Blob and returns two artifacts:

- `document_md`: concise English description and summary, plus a Markdown table of relevant structured values.
- `document_data`: source-grounded JSON using compact positional rows for facts, events, references, and signals, plus string uncertainties.

Supported binary source formats are PDF (`application/pdf`), PNG (`image/png`), and JPEG (`image/jpeg`). The plugin stages original bytes unchanged. It uses the original basename when supplied; if `import_context.original_filename` is missing or blank, staged names retain usable suffixes: `document.pdf`, `document.png`, or `document.jpg`. Codex can inspect PDFs with available local shell tools; for PNG/JPEG, the extraction prompt tells Codex to call `view_image` on the supplied local path. There is no added conversion or image-extraction pipeline. For images with no readable text or structured data, the prompt asks for a visual description only and empty structured-data categories, without invented text or facts. Tool work can require multiple model calls within one turn.

The second-turn schema fixes each row's order and length; extraction prompts specify how to ground values in the source:

- `facts`: `[key, value, evidence]`
- `events`: `[action, date, details, evidence]`
- `references`: `[kind, value, title, relation, evidence]`
- `signals`: `[kind, value, evidence]`
- `uncertainties`: strings, not rows

All row values are strings except an event's `date` and a reference's `title` or `relation`, which may be `null`. Local validation enforces exact row lengths and position-specific types. Empty categories remain empty arrays. For example, a fact is `["invoice_number", "82000037665", "Invoice No."]`. The JSON artifact records schema `memoryd.document_data.v2`; the former object-row schema is no longer emitted.

The plugin requires Python 3.12+, `uv`, and a local `codex` executable. Run it through memoryd with a Codex app-server model, for example:

```sh
go run ./server/cmd/mem-understand ./document.pdf \
  --plugin="$(pwd)/plugins/codex-extractor/mem-understand-codex-extractor" \
  --model-provider=codex_app_server \
  --model-name=gpt-6-luna \
  --model-effort=medium \
  --app-server-command="$(command -v codex)"
```

Public smoke inputs: [W3C dummy PDF](https://www.w3.org/WAI/ER/tests/xhtml/testfiles/resources/pdf/dummy.pdf) and [Wikimedia flower JPEG](https://upload.wikimedia.org/wikipedia/commons/3/3f/JPEG_example_flower.jpg). On macOS, use the built-in `sips` to produce PNG from that same image:

```sh
mkdir -p /tmp/codex-extractor-smoke
curl -fL https://www.w3.org/WAI/ER/tests/xhtml/testfiles/resources/pdf/dummy.pdf \
  -o /tmp/codex-extractor-smoke/dummy.pdf
curl -fL https://upload.wikimedia.org/wikipedia/commons/3/3f/JPEG_example_flower.jpg \
  -o /tmp/codex-extractor-smoke/flower.jpg
sips -s format png /tmp/codex-extractor-smoke/flower.jpg \
  --out /tmp/codex-extractor-smoke/flower.png

for source in /tmp/codex-extractor-smoke/dummy.pdf \
  /tmp/codex-extractor-smoke/flower.jpg \
  /tmp/codex-extractor-smoke/flower.png; do
  go run ./server/cmd/mem-understand "$source" \
    --plugin="$(pwd)/plugins/codex-extractor/mem-understand-codex-extractor" \
    --model-provider=codex_app_server \
    --model-name=gpt-6-luna \
    --model-effort=medium \
    --app-server-command="$(command -v codex)"
done
```

This requires Go, Python 3.12+, `uv`, `curl`, macOS `sips` for PNG conversion, and a locally installed, signed-in `codex` executable. Replace model flags with an available Codex model if needed.

The plugin explicitly enables `shell_tool`, `unified_exec`, and `view_image`; for PNG/JPEG, Codex invokes `view_image` with the staged local path. It disables skill search, multi-agent, plugin, app, browser, computer-use, and image-generation features, and disables web search. Each turn uses the temporary directory as its only writable root with network access disabled. Codex uses the user's existing local sign-in. Token totals and per-turn usage are included when the app-server reports complete snapshots.

Startup first uses an app-server process to discover skill paths and effective MCP settings without starting any model turns. It then restarts app-server with transient configuration disabling every discovered skill (including PDF) and configured MCP server. It checks that no skills remain enabled before extraction. This keeps the skill catalog out of the model conversation and leaves the user's normal Codex settings unchanged. See [Codex skill configuration](https://learn.chatgpt.com/docs/build-skills).

MCP exclusions use one TOML inline-table override, so server names remain literal keys (including hyphens or dots) and existing transport settings are preserved. Quoting names inside dotted CLI override paths would instead create malformed server entries.

The extraction thread uses empty base and additional developer instructions; project instruction loading is disabled. The extraction prompts and schema supply the task instructions. Codex still supplies tool definitions and runtime permission context. The runtime log shows enabled file tools and exclusion counts without dumping private configuration.

## Workflow logging

The plugin writes human-readable logs to stderr and writes only the v1 protocol JSON result to stdout. Concise stage lines show request receipt and validation, app-server start and stop, source staging, thread readiness, turn start and completion, output validation, result writing, and success or failure. The request log includes import context and the selected model and reasoning effort. Each turn shows its exact prompt, local document path text input, and schema when present. Assistant responses stream live; a completed response is printed only when it was not already shown in full. Command logs show command and working directory at start, then status, exit code, duration, and aggregated output at completion; collected output-delta text is used when aggregated output is unavailable. Image-view logs show start/completion and path without image bytes.

MCP tool events include server and tool names, arguments when available, progress messages, results, and errors. Arguments and structured content are formatted for reading. Text result blocks appear as multiline text. Image, audio, and binary resource blocks show their type, MIME type, and encoded size without printing their payload. A missing tool result is labeled explicitly. The log omits the Blob's base64 body and does not copy raw JSON-RPC messages.

Usage updates show one compact count line for each distinct app-server snapshot, using the last model call's counts. Each turn then gets one duration, usage, and cost summary, followed by one cumulative usage and cost summary. Repeated identical snapshots are suppressed, and missing counts or estimates include a short reason.

Cost estimates are API-equivalent estimates, not subscription bills. The plugin estimates only for `gpt-6-luna`, using published Standard short-context rates per 1M tokens: input $0.10, cached input $0.01, cache-write input $0.125, and output $0.50. It assumes each model request uses the short-context tier and never infers a long-context tier by summing requests. Reasoning tokens are already included in output tokens. The rates were checked 2026-09-30 in the [gpt-6-luna model documentation](https://developers.openai.com/api/docs/models/gpt-6-luna). The cumulative estimate is returned as `cost_estimate`; per-turn estimates appear in stderr to preserve the v1 `turn_usage` shape. Unknown models, mixed models across turns, and invalid or missing usage leave the estimate unavailable with a concise log reason.

`mem-understand` displays the log live and saves it as `stderr` in its capture directory. Open that file directly to inspect the run. Logs include import context, exact prompts, assistant output, MCP arguments and results, and app-server stderr; any of these may contain source content. Treat captured stderr as private source data.
