# Search

Search retrieves whole Memories from across the Vault using lexical matching over stored content. Document Understanding can supply model-generated descriptions and extracted text beforehand; Search Planning and search execution are deterministic and model-free.

## One search interface

All search tools use the same server API. The web UI and `mem-search` share Search Planning, matching, ranking, and pagination. Neither accesses storage directly or has a tool-specific search path. Search behavior belongs to the server.

The [OpenAPI specification](../api/openapi.yaml) is the interface source of truth. [README](../README.md#search) covers client usage.

## Searchable content

Each Memory contributes:

- Its original filename, available Import Context paths, and saved user note.
- Original Blob text when its Media Type is `text/*`, its content is valid UTF-8, and it contains no NUL bytes.
- Supported Derived Content from its active Understanding Run: `text/*` artifacts contribute text; `application/json` artifacts contribute string, number, and boolean values.

JSON extraction recursively collects scalar values and excludes reserved metadata subtrees, including provenance, processing details, warnings, and logs. This is generic value extraction rather than interpretation of a particular Fact schema. Malformed JSON remains a retained artifact but contributes no search values.

Only the active Run contributes Derived Content. Historical Runs and execution logs are excluded. Unsupported original or derived formats remain searchable through the Memory's other fields. Several matching fields or artifacts still produce one whole-Memory result.

## Storage and lifecycle

Search uses two SQLite FTS5 indexes in the Vault database: a word/stem index that stores searchable text, and a trigram index that reuses that text for substring matching. Each Memory has one projection row.

Memory metadata, saved notes, active-Run selection, and stored Blobs are authoritative. Search indexes are rebuildable projections of that state. Queries read the projections rather than opening Blobs.

Imports, note changes, deletions, and successful Understanding activation update both indexes in the same database transaction as the corresponding durable state change. A successful Run atomically replaces searchable Derived Content; a failed Rebuild preserves the previous active content. Saved notes persist independently of Runs and follow their own updates. There is no separate indexing worker or queue.

**Server startup triggers reconstruction of missing search state.** Missing indexes are created and populated; missing Memory entries and unindexed active Runs are backfilled from stored content. Supported projection upgrades reuse already-indexed original text. Startup tracks which active Run has been indexed, so already-indexed artifacts are not reread routinely. Reconstruction requires no reimport, model calls, or changes to immutable Runs.

This recovery covers missing projection state and supported projection upgrades. It does not provide general corruption repair or migrate incompatible authoritative schemas. Missing or corrupt Blobs required for backfill cause startup to fail.

## Search Planning and matching

Search Planning trims the query, splits it into Unicode words and numbers, and lowercases the resulting terms. Punctuation separates terms. The visible Query Plan contains the submitted query and these mandatory terms. A query without any words or numbers is rejected.

Every term must match somewhere in the Memory's searchable fields. Each term may match a whole word or English stem, or a case-insensitive Unicode substring. Different terms may use different fields and matching modes.

Connectors, operator-like words, and years remain literal mandatory terms. For example, `passport 2026` requires both text terms; `passport from 2026` also requires `from`.

## Ordering and excerpts

Results sort by:

1. Complete word/English-stem matches before substring-dependent matches.
2. Lower BM25 score within each tier.
3. Memory ID for score ties.

Complete word/stem matches use word-index BM25. Substring-dependent matches use trigram-index BM25 for matching terms of at least three Unicode characters. If that result has no indexed substring match, it uses word-index BM25; if it also has no word match, its score is `0`. Each result exposes its match tier and score. Interpret the score within its tier and scoring index; the tier determines which group comes first.

The total includes the full combined matching set before pagination. Excerpts are generated for the selected page. They contain source text split into highlighted and unhighlighted segments, rendered as text by clients. Matching original text takes priority, then saved notes, then Derived Content. Filename/path-only matches may have no excerpt. An excerpt shows a matching passage; other required terms can occur elsewhere in the Memory.

## Contract and pagination

`GET /api/v0/memories/search` accepts a query, page limit, and optional continuation cursor. The response contains a Query Plan, total match count, ordered whole-Memory results with ranking information and excerpts, and a next cursor when more results exist.

Pages default to 50 results and accept limits from 1 to 100. A continuation must use the same trimmed query. Each page reads a consistent database state, but continuations do not retain a snapshot across requests. Complete paging without duplicates or omissions requires an unchanged Vault; concurrent imports, deletions, note changes, or Rebuilds can shift results between pages. Search responses use `Cache-Control: no-store`.

The web UI appends pages with **Load more** and retains loaded results when returning from Memory details. `mem-search -n N` follows pages up to the requested count; `--all` follows to exhaustion. Both use the same continuation contract.

## Known limits

- Retrieval requires matching vocabulary in searchable content. There are no synonyms, relationship aliases, translation, transliteration, Russian morphology, embeddings, or automatic query relaxation.
- Fact filters and temporal interpretation remain deferred. Structured dates currently participate as text values.
- One/two-character substrings scan projection text. Cost grows with text volume, particularly for broad or absent short terms.
- Each page repeats match counting and ranking work. Continuations use offsets, so deep pagination is not constant-cost.
- Startup recovery depends on intact authoritative state and the Blobs needed for reconstruction.

## Where to start digging

- **Interface:** start with the search operation and `SearchPage`, `SearchQueryPlan`, and `SearchHit` schemas in [OpenAPI](../api/openapi.yaml). Inspect a real response with `mem-search 'passport 2026'`; the Query Plan shows exactly which terms the server requires.
- **Storage:** `memory_search` stores the five searchable fields (`filename`, `paths`, `body`, `note`, `derived`) using FTS5 `porter unicode61`. `memory_search_fragments` uses FTS5 `trigram` with `memory_search` as its external content source. `memory_search_derived` records which active Run supplied each Memory's indexed Derived Content.
- **Retrieval mechanics:** word/stem and substring candidates are combined for each term, then intersected across terms. Ranking selects the page before excerpt generation. Continuations carry a version, trimmed query, and offset; clients should treat them as opaque tokens.
- **Understanding input:** the [Understanding Plugin interface](understanding-plugin-v2.md) explains how artifacts reach the Vault. Search reads supported artifacts from the selected active Run, independently of which plugin produced them.
