# memoryd MVP design snapshot

Status: authoritative snapshot of decisions from the current design interview. Earlier notes outside this repository are brainstorming material only.

## Goal

Import personal files into a durable local Vault, understand them as well as currently available tools allow, and retrieve whole Memories using natural-language queries over Memory metadata, Derived Content, structured Facts and extracted text.

## Product boundary

### In scope

- Run a single-user Vault on a trusted local machine.
- Import individual Blobs up to 100 MiB (104857600 bytes) through the CLI or web UI.
- Preserve each imported Blob durably as a Memory with its Import Context.
- Perform best-effort Document Understanding in the background.
- Browse Memories and inspect their metadata, Derived Content, Facts, and Understanding Run.
- Search using natural language and inspect the resulting Query Plan.
- Open or download the whole original Memory.
- Accept opaque or partially understood content as a valid result.

### Out of scope

- Memory lifecycle: Rebuild, deletion, garbage collection, and user Fact corrections.
- Understanding extensibility: Understanding Plugins and Codex enhancement.
- Search sophistication: embeddings, relevance ranking, query relaxation, relationship aliases, and cross-language retrieval.
- Productization: multiple users, authentication, remote access, and production-grade UI polish.

### Demonstration scenarios

The first useful demonstration imports personal files, then answers variations of:

- Find all Tasleem bills from 2026.
- Find a particular person's passport photo when that person is named explicitly.
- Show PDFs created during a specified period.

## Import and Memory identity

- The server exposes a single-file import contract.
- One imported file is one user-visible Memory. Internal pages, chunks, attachments, or sections are not separately returned.
- The server hashes each candidate before creating a Memory. Existing content hashes are rejected as Duplicates and do not enrich the existing Memory. The duplicate response identifies the existing Memory.
- Successful content is copied into content-addressed storage. The Memory never depends on the original path remaining valid.
- The server detects Media Type from Blob content. When detection result is generic, fall back to a client supplied, valid, specific multipart part `Content-Type`; absent or generic declarations leave the generic result. Blob storage does not depend on Media Type.
- Original filename, any available import path information, media type, byte size, content hash, and available filesystem timestamps are retained as provenance. Browsers may not expose absolute local paths.
- The path may influence import-time understanding, but memoryd does not subsequently read through that path.

## Storage and durability

- A Memory references one immutable Blob addressed by its content hash.
- The filesystem content-addressed store holds Blob bytes. SQLite is authoritative for Memories, metadata, Facts, Understanding Runs, active-Run selection, and logs.
- SQLite records the operational progress of understanding attempts. On startup, memoryd retries work interrupted by shutdown.

## Document Understanding

- Document Understanding makes a best-effort attempt for every imported Blob and produces Derived Content and Facts.
- An opaque or partially understood Blob is a valid result. An Understanding Run may be sparse and contain only information derived from basic Blob properties and Import Context.
- Each successful interpretation creates an immutable Understanding Run.
- A coherent Run may include warnings. An understanding attempt fails only when an operational issue prevents it from committing a coherent Run; the failure produces an informative execution log and does not invalidate the Memory.
- After the Blob and Memory commit, Document Understanding may run durably in the background, independently of the import request and client connection.
- Local Document Understanding runs have no explicit resource limits in the MVP.
- A Fact has a name, category, origin, type, and value. Deeper Fact semantics remain an open design question.

## Search

- Search Planning converts a natural-language query into a visible Query Plan.
- Model access for Search Planning is configured through the `search_planning` Model Task Category. The configured provider never falls back automatically in the MVP.
- Known Fact constraints become exact filters. Remaining or unresolved terms become mandatory full-text search terms.
- The plan executes immediately and is displayed to the user.
- Constraints are not silently removed. Query relaxation is out of scope.
- Search initially uses typed Fact filtering plus full-text search over filenames, metadata, and Derived Content. Embeddings are deferred.
- Parsers may declare kind-specific fields and date semantics. For example, a Tasleem year constraint should prefer billing period rather than treating all dates as interchangeable.
- Results return whole Memories with useful metadata and an open/download action.
- Relationship aliases are not inferred. A query containing `my wife` is treated as literal full text and may return no results; the user must rephrase it with a name.

## Interfaces

Both clients use the server API; neither accesses storage directly.

### CLI

The `cli/mem` tool provides `get`, `put`, `list`, and `info` operations.

### Web UI

The MVP web UI is a basic interface that will evolve through use. Its initial capabilities are:

- The main view explores every Memory known to the Vault.
- Selecting a Memory shows its preview or download action, Import Context, all Fact key/value pairs and provenance, Derived Content, and active Understanding Run.
- Import shows the selected file, upload progress, and background understanding progress.
- Search shows the original query, interpreted Query Plan, and matching Memories.
- Understanding details expose the active Run and available processing information.
- The UI is the human-readable view over the Vault; the filesystem CAS has no filename mirror.
- Production-grade polish is not required.

## Implementation constraints

- Implement the main server in Go, with SQLite/FTS5 and filesystem content-addressed storage.
- Use Ollama as the initial model runtime for Document Understanding and Search Planning.
- The Go server uses `destel/rill` to bound concurrent understanding across requests and startup recovery, but introduces no persistent queue table, separate worker service, or external queue.
- Bind only to loopback and require no application login in the MVP.
- Treat a checked-in `openapi.yaml` as the API source of truth and generate Go handler interfaces and request/response types from it.

## Open design work

### Document Understanding

- Inputs, outputs, methods, supported formats, and failure behavior
- Fact cardinality, schemas, and indexes

### API

- Import and background understanding status and progress
- Browsing, Memory details, and search

### Storage

- SQLite schema and FTS5 projection design
- CAS layout, hashing algorithm, atomic writes, and integrity verification

### Search

- Ollama model selection and Query Plan schema

### UI

- Import progress, background understanding, search interpretation, results, and Memory details

### Operations

- Backup, restore, SQLite recovery, schema migration, observability, and test strategy
