# memoryd MVP design snapshot

Status: authoritative snapshot of decisions from the current design interview. Earlier notes outside this repository are brainstorming material only.

## Goal

Import personal files into a durable local Vault, understand them as well as currently available tools allow, and retrieve whole Memories using natural-language queries over Memory metadata, Derived Content, structured Facts and extracted text.

The first useful demonstration imports personal files, then answers variations of:

- Find all Tasleem bills from 2026.
- Find a particular person's passport photo when that person is named explicitly.
- Show PDFs created during a specified period.

## Explicit MVP boundaries

- One imported file is one user-visible Memory. Internal pages, chunks, attachments, or sections are not separately returned.
- Any Blob within the size limit can be imported.
- The MVP includes Document Understanding. Its supported formats, behavior, methods, and limits remain to be designed.
- Understanding Plugins, Codex enhancement, cross-language retrieval, embeddings, relevance ranking, query relaxation, user Fact corrections, deletion, and garbage collection are deferred.
- Relationship aliases are not inferred. A query containing `my wife` is treated as literal full text and may return no results; the user must rephrase it with a name.

## Import

- The server exposes a single-file import contract.
- The initial importing client is a `cli/mem` tool allowing get/put/info/list subcommands.
- The server hashes each candidate before creating a Memory. Existing content hashes are rejected as Duplicates and do not enrich the existing Memory. The duplicate response identifies the existing Memory.
- Successful content is copied into content-addressed storage. The Memory never depends on the original path remaining valid.
- The server detects Media Type from Blob content. When detection result is generic, fall back to a client supplied, valid, specific multipart part `Content-Type`; absent or generic declarations leave the generic result. Blob storage does not depend on Media Type.
- Original filename, any available import path information, media type, byte size, content hash, and available filesystem timestamps are retained as provenance. Browsers may not expose absolute local paths.
- The path may influence import-time understanding, but memoryd does not subsequently read through that path.

## Storage and understanding

- A Memory references one immutable Blob addressed by its content hash.
- Document Understanding produces the MVP's Derived Content and Facts. Its implementation is an open design question.
- Each successful interpretation creates an immutable Understanding Run. A Rebuild stages a complete new Run and atomically activates it; earlier successful Runs remain inspectable but only the active Run participates in search.
- Failed understanding attempts do not create Runs. They produce informative execution logs and leave the previous active Run unchanged.
- An Understanding Run may complete with explicit warnings when a coherent, usable result can still be committed. `Failed` is reserved for an attempt that cannot commit such a Run.
- SQLite records each Memory's Understanding State as `InProgress`, `Done`, or `Failed`. On startup, memoryd selects every `InProgress` Memory and retries its understanding work.
- A single-file import request remains open while understanding runs. After the Blob and `InProgress` Memory row commit, the work is detached from client cancellation; a disconnected client does not cancel it. Server-side progress and the terminal result use a streamed event response, while upload-byte progress is measured by the browser.
- A Fact has a name, category, origin, type, and value. Deeper Fact semantics remain an open design question.

## Query understanding

- Model access for query understanding is configured through the `query_understanding` Model Task Category.
- The configured provider never falls back automatically in the MVP.

## Search

- A configured model converts natural language into a visible Query Plan.
- Known Fact constraints become exact filters. Remaining or unresolved terms become mandatory full-text search terms.
- The plan executes immediately and is displayed to the user.
- Constraints are not silently removed. Query relaxation is out of scope.
- Search initially uses typed Fact filtering plus full-text search over filenames, metadata, and Derived Content. Embeddings are deferred.
- Parsers may declare kind-specific fields and date semantics. For example, a Tasleem year constraint should prefer billing period rather than treating all dates as interchangeable.
- Results return whole Memories with useful metadata and an open/download action.

## First integrations

- Use an Ollama chat-capable model for query understanding after selecting and installing one; the currently installed `embeddinggemma` model cannot perform that task.
- Implement the main server in Go, with SQLite/FTS5 and filesystem content-addressed storage.
- Provide a basic browser client against the local server API.
- The Go server uses `destel/rill` to bound concurrent understanding across requests and startup recovery, but introduces no persistent queue table, separate worker service, or external queue.
- Bind only to loopback and require no application login in the MVP.
- Treat a checked-in `openapi.yaml` as the API source of truth and generate Go handler interfaces and request/response types from it. The initial browser client may remain handwritten.

## Web UI

- The main view explores every Memory known to the Vault.
- Selecting a Memory shows its preview or download action, Import Context, all Fact key/value pairs and provenance, Derived Content, and active Understanding Run.
- Import shows the selected file and its upload and understanding progress.
- Search shows the original query, interpreted Query Plan, and matching Memories.
- Understanding details expose the active Run and available processing information.
- The UI is the human-readable view over the Vault; the filesystem CAS has no filename mirror.

## Open implementation details

- Document Understanding inputs, outputs, implementation, supported formats, limits, and failure behavior
- Exact OpenAPI event schemas for the single streaming import request, browsing, detail, Rebuild, and search
- SQLite schema and FTS5 projection design
- CAS layout, hashing algorithm, atomic writes, and integrity verification
- Fact cardinality, schemas, and indexes
- Ollama model selection and Query Plan schema
- UI states for import progress, search interpretation, results, and Memory details
- Backup, restore, SQLite recovery, schema migration, observability, and test strategy
