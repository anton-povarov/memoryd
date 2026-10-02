# memoryd MVP design snapshot

Status: design snapshot with the implemented keyword-search slice described below. Broader Fact and temporal search remain design goals.

## Goal

Import personal files into a durable local Vault, understand them as well as currently available tools allow, and retrieve whole Memories using natural-language queries over Memory metadata, Derived Content, structured Facts and extracted text.

## Product boundary

### In scope

- Run a single-user Vault on a trusted local machine.
- Import individual Blobs up to 100 MiB (104857600 bytes) through the CLI or web UI.
- Preserve each imported Blob durably as a Memory with its Import Context.
- Perform best-effort Document Understanding in the background.
- Produce searchable Derived Content from PDFs as the first Document Understanding capability.
- Browse Memories and inspect their metadata, Derived Content, Facts, and Understanding Run.
- Manually Rebuild a Memory's Understanding using the current plugin while preserving its original Blob and last successful Run on failure.
- Search the whole Vault using keywords and inspect the resulting Query Plan.
- Open or download the whole original Memory.
- Accept opaque or partially understood content as a valid result.

### Out of scope

- Memory lifecycle beyond manual Rebuild and deletion: garbage collection and user Fact corrections.
- Understanding extensibility: plugin discovery, dynamic installation, and Codex enhancement.
- Search sophistication beyond BM25: embeddings, query relaxation, relationship aliases, and cross-language retrieval.
- Productization: multiple users, authentication, remote access, and production-grade UI polish.

### Demonstration scenarios

The first useful demonstration imports personal files, then answers variations of:

- Find all Tasleem bills from 2026.
- Find a particular person's passport photo when that person is named explicitly.
- Show PDFs created during a specified period.

Fact and temporal demonstration queries remain future goals. The current slice
treats every query word and number as literal mandatory text, including `2026`.

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
- SQLite records only terminal Understanding outcomes. Pending work and polling handles are process-local; restart drops them and startup schedules nothing.
- A rebuildable FTS5 projection contains filenames, available Import Context paths, supported original text, and saved notes. Imports, note changes, and deletions update it in the same database mutation; compatible existing Memories are backfilled without reimport or Document Understanding.

## Document Understanding

- Document Understanding makes a best-effort attempt for every imported Blob and may produce Derived Content and Facts.
- An opaque or partially understood Blob is a valid result. An Understanding Run may be sparse and contain only information derived from basic Blob properties and Import Context.
- The first extraction increment targets PDFs and does not need to assert structured Facts. A plugin may extract embedded text, perform OCR, or describe visual content. Source text and generated descriptions remain distinguishable Derived Content.
- Each successful interpretation creates an immutable Understanding Run.
- A coherent Run may include warnings. An understanding attempt fails only when an operational issue prevents it from committing a coherent Run; the failure produces an informative execution log and does not invalidate the Memory.
- After the Blob and Memory commit, Document Understanding runs best effort in a process-local queue, independently of the client connection. Admission failure does not undo the import; explicit Rebuild can retry.
- One configured process limit covers imports and manual Rebuilds. Rebuild returns a polling handle with 202; competing queued/running work returns 409 with the existing handle. Terminal handles remain until superseded, deletion, or restart. Invalid handles return 404.
- A Fact has a name, category, origin, type, and value. Deeper Fact semantics remain an open design question.

## Search

- Current Search Planning is deterministic and model-free: trim the query, split Unicode words/numbers, lowercase them, and require every term. Operator-like words, connectors, and numeric years remain literal text.
- The visible Query Plan contains the submitted query and mandatory text terms. No Fact or date interpretation is performed yet; `search_planning` model routing remains unimplemented.
- Search covers original filenames, available Import Context paths, valid UTF-8 `text/*` original Blobs without NUL bytes, and saved user notes across the whole Vault. Other formats retain metadata/note search. Active Derived Content is not searched in this slice.
- Each mandatory term may match a whole word/English stem or a case-insensitive Unicode substring across current searchable fields. Complete whole-word/stem matches rank first by BM25; fragment-dependent matches follow by BM25 for matching words, then deterministic identity. Fragment-only ties use identity. Totals cover all modes before limiting, and each whole Memory appears once. Filenames and matching body/note excerpts are safely highlighted; metadata-only matches have no fabricated body excerpt.
- Native FTS5 trigram indexing supports fragments of at least three Unicode characters; one/two-character fragments use a Unicode-aware scan of indexed text. Both indexes update transactionally and the fragment index backfills from existing projection text without reimport or models.
- The plan executes only on explicit submission and remains visible with empty results. Constraints are not silently removed. Models, aliases, translation, query relaxation, and embeddings are not used.
- HTTP and `mem-search` return a limited first page with total matches independent of the positive int64 limit (default 50). No pagination or `--all` exists yet. Search responses forbid caching so committed changes are visible on repeated queries.
- Fact filtering, active Derived Content, and calendar/relative date interpretation belong to later slices. Future parsers may declare kind-specific date semantics rather than treating all dates as interchangeable.

## Interfaces

Both clients use the server API; neither accesses storage directly.

### CLI

The `mem` tool provides `get`, `put`, `list`, `info`, and `delete`. The standalone `mem-search [--server ADDRESS] [-n N] <quoted phrase>` executable prints the complete first-page Search response as JSON and shares server selection with `mem`.

### Web UI

The MVP web UI is a basic interface that will evolve through use. Its initial capabilities are:

- The main view explores every Memory known to the Vault.
- Selecting a Memory shows its preview or download action, Import Context, all Fact key/value pairs and provenance, Derived Content, and active Understanding Run.
- Import shows the selected file, upload progress, and background understanding progress.
- Search shows the original query, interpreted Query Plan, and matching Memories.
- Understanding details expose the active Run and available processing information.
- The UI is the human-readable view over the Vault; the filesystem CAS has no filename mirror.
- Production-grade polish is not required.

### Plugin development tool

The standalone `mem-understand` tool runs an Understanding Plugin executable supplied by path against a local file without a server or server configuration, prints its interpreted result, and saves the raw protocol exchange for inspection. It is not a Vault client.

## Implementation constraints

- Implement the main server in Go, with SQLite/FTS5 and filesystem content-addressed storage.
- Model-assisted Document Understanding uses centrally configured providers. The implemented search slice requires no model runtime.
- The Go server uses `destel/rill` to bound concurrent understanding across requests and startup recovery, but introduces no persistent queue table, separate worker service, or external queue.
- Initial Understanding Plugins are configured executable paths launched as child processes by memoryd. They exchange JSON over standard streams. Plugin discovery, manifests, and a general plugin framework are deferred.
- Require no application login in the MVP.
- Treat a checked-in `openapi.yaml` as the API source of truth and generate Go handler interfaces and request/response types from it.

## Open design work

### Document Understanding

- Inputs, outputs, methods, supported formats, and failure behavior
- Fact cardinality, schemas, and indexes

### API

- Import and background understanding status and progress
- Fact/date search and pagination beyond the implemented keyword operation

### Storage

- Backup and recovery of authoritative SQLite/CAS data; the implemented FTS5 projection is derived and rebuildable

### Search

- Active Derived Content, temporal interpretation, and stable search pagination

### UI

- Import progress, background understanding, search interpretation, results, and Memory details

### Operations

- Backup, restore, SQLite recovery, schema migration, observability, and test strategy
