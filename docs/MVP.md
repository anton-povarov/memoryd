# memoryd MVP design snapshot

Status: authoritative snapshot of decisions from the current design interview. Earlier notes outside this repository are brainstorming material only.

## Goal

Import personal files into a durable local Vault, understand them as well as currently available tools allow, and retrieve whole Memories using natural-language queries over structured Facts and extracted text.

The first useful demonstration imports a directory containing PDFs, JPEGs, and PNGs, then answers variations of:

- Find all Tasleem bills from 2026.
- Find a particular person's passport photo when that person is named explicitly.
- Show PDFs created during a specified period.

## Explicit MVP boundaries

- One imported file is one user-visible Memory. Internal pages, chunks, attachments, or sections are not separately returned.
- Supported content formats are PDF, JPEG, and PNG.
- Cross-language retrieval, embeddings, relevance ranking, query relaxation, user Fact corrections, deletion, and garbage collection are deferred.
- Relationship aliases are not inferred. A query containing `my wife` is treated as literal full text and may return no results; the user must rephrase it with a name.
- Results are reverse-sorted by the best available original filesystem creation timestamp, then modification timestamp, then import timestamp.

## Import

- The importing client recursively enumerates regular files in deterministic path order.
- It skips hidden files and directories, `.git`, `.DS_Store`, symlinks, the Vault itself, and currently unsupported formats.
- The initial importing client is browser-based and uploads one file at a time. On failure it offers retry, skip, or abort; abort preserves earlier successes, stops later uploads, and does not cancel understanding already started for the current durable Memory.
- Browser directory selection yields a flat file list with paths relative to the selected directory. The client filters and deterministically sorts that list, displays it, and manages sequential upload locally without creating a server-side batch.
- The server hashes each candidate before creating a Memory. Existing content hashes are rejected as Duplicates and do not enrich the existing Memory. The duplicate response identifies the existing Memory and its Understanding State so a client can recover from an ambiguous interrupted upload.
- Successful content is copied into content-addressed storage. The Memory never depends on the original path remaining valid.
- Original filename, browser-provided relative path, any available full import path, media type, byte size, content hash, and available filesystem timestamps are retained as Facts or provenance. Browsers may not expose absolute local paths.
- The path may influence import-time understanding, but memoryd does not subsequently read through that path.

## Storage and understanding

- A Memory references one immutable Blob addressed by its content hash.
- File metadata extraction always runs.
- General Extraction runs whenever a suitable local tool exists.
- The highest-confidence, most-specific matching Understanding Plugin may then add Specialized Enrichment. A failed specialized plugin does not erase general results.
- Understanding Plugins are versioned external processes with manifest, probe, and enrich operations using structured input/output.
- Each successful interpretation creates an immutable Understanding Run. A Rebuild stages a complete new Run and atomically activates it; earlier successful Runs remain inspectable but only the active Run participates in search.
- Failed understanding attempts do not create Runs. They produce informative execution logs and leave the previous active Run unchanged.
- An Understanding Run may complete with explicit warnings when optional enrichment fails. `Failed` is reserved for an attempt that cannot commit any coherent, usable Run.
- SQLite records each Memory's Understanding State as `InProgress`, `Done`, or `Failed`. On startup, memoryd selects every `InProgress` Memory and retries its understanding work.
- A single-file import request remains open while understanding runs. After the Blob and `InProgress` Memory row commit, the work is detached from client cancellation; a disconnected client does not cancel it. Server-side progress and the terminal result use a streamed event response, while upload-byte progress is measured by the browser.
- Facts use semantic or provenance namespaces and coexist rather than overwriting one another. Examples include `exif/created_at`, `bill/issue_date`, and `passport/valid_through_date`.

## Model-assisted work

- Model access is configured by Model Task Category. Example mappings are `query_understanding -> ollama` and `hard_content_extraction -> codex_app_server`.
- Providers never fall back automatically in the MVP.
- Codex App Server uses managed ChatGPT OAuth. The first integration deliberately relies on the Codex harness and its local tools to inspect permitted files and return structured understanding.
- Hard content extraction through Codex is never automatic. After file metadata, General Extraction, and Specialized Enrichment, the user must explicitly request a Codex-assisted step through a client.
- Explicit Codex enhancement creates a new complete Understanding Run containing the regular pipeline result plus Codex enrichment, then atomically activates it. An equivalent prior Codex-enhanced Run does not prohibit another attempt, but the client asks for confirmation before repeating it.
- Full original PDFs are not uploaded as native model file inputs.
- The initial configurable limit for cloud-bound input is 5 MiB. Files above that limit still enter the Vault and receive local metadata, General Extraction, and any local Specialized Enrichment; cloud-assisted understanding is skipped when memoryd cannot prepare an eligible input.
- No page, character, call-count, or estimated-token ledger is implemented initially.
- Processing logs retain exact plugin stdout and stderr, final raw model output, the structured result accepted by memoryd, and meaningful lifecycle, tool, error, and usage events. OAuth credentials, secrets, inaccessible hidden reasoning, and redundant transport-level deltas are excluded. Each attempt has an initially configurable 10 MiB log ceiling; truncation is explicit and recorded.

## Search

- A configured model converts natural language into a visible Query Plan.
- Known Fact constraints become exact filters. Remaining or unresolved terms become mandatory full-text search terms.
- The plan executes immediately and is displayed to the user.
- Constraints are not silently removed. Query relaxation is out of scope.
- Search initially uses typed Fact filtering plus full-text search over filenames, metadata, and Derived Content. Embeddings are deferred.
- Parsers may declare kind-specific fields and date semantics. For example, a Tasleem year constraint should prefer billing period rather than treating all dates as interchangeable.
- Results return whole Memories with useful metadata and an open/download action.

## First integrations

- Adapt the existing Tasleem parser as the first Understanding Plugin.
- Use an Ollama chat-capable model for query understanding after selecting and installing one; the currently installed `embeddinggemma` model cannot perform that task.
- Use Codex App Server through ChatGPT OAuth for difficult content understanding.
- Implement the main server in Go, with SQLite/FTS5, filesystem content-addressed storage, and subprocess Understanding Plugins written in any language.
- Provide a basic browser client against the local server API.
- Upload files sequentially from the browser and wait for each file's understanding attempt before uploading the next. The Go server uses `destel/rill` to bound concurrent understanding across requests and startup recovery, but introduces no persistent queue table, separate worker service, or external queue.
- Bind only to loopback and require no application login in the MVP.
- Treat a checked-in `openapi.yaml` as the API source of truth and generate Go handler interfaces and request/response types from it. The initial browser client may remain handwritten.

## Web UI

- The main view explores every Memory known to the Vault.
- Selecting a Memory shows its preview or download action, Import Context, all Fact key/value pairs and provenance, Derived Content, and active Understanding Run.
- Import shows the browser-selected file list and the current sequential upload, with retry, skip, and abort controls managed in the page.
- Search shows the original query, interpreted Query Plan, and matching Memories.
- Understanding details expose plugin/model versions and processing logs.
- The UI is the human-readable view over the Vault; the filesystem CAS has no filename mirror.

## Known MVP risks

1. **Cloud-bound bytes do not precisely predict model cost.** Compressed documents and image dimensions can produce very different token use for the same byte count.
2. **Codex App Server may mediate tool results internally.** memoryd can limit the files and prompt material it exposes to the worker, but strict accounting of every byte entering model context may require later instrumentation.
3. **Agent behavior is not a parser contract.** Codex may choose different tools or inspection depth as models, prompts, and skills evolve. Structured output, constrained permissions, regression fixtures, and execution logs will be important.
4. **Agentic document processing expands the trust boundary.** Imported content can contain prompt injection. The Codex worker should have read-only access to its staged input and minimal tools, filesystem scope, and network authority.
5. **Logs may duplicate sensitive content.** Passport text, paths, tool output, and model responses require size limits, redaction rules, and a retention policy.
6. **A byte ceiling is not a hard token ceiling.** The MVP intentionally accepts this uncertainty and will observe real usage before adding a more elaborate budget planner.

## Open implementation details

- Exact OpenAPI event schemas for the single streaming import request, browsing, detail, Rebuild, Codex enhancement, and search
- SQLite schema and FTS5 projection design
- CAS layout, hashing algorithm, atomic writes, and integrity verification
- Fact value types, cardinality, schemas, and indexes
- Plugin manifest and process protocol
- Codex worker sandbox and structured-output contract
- Ollama model selection and Query Plan schema
- UI states for import, retry/skip/abort, search interpretation, results, and Memory details
- Backup, restore, SQLite recovery, schema migration, observability, and test strategy
