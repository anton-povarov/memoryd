# Understanding Runs and retrieval projections

Status: research note; synthesis of an experiment, not a product specification  
Date: 2026-09-18  
Scope: what a first understanding pipeline should preserve, promote, and index across heterogeneous personal documents

## Executive answer

The experiment supports a layered interpretation of a Memory:

1. **Derived Content** preserves source-shaped output: OCR or native text, page and section boundaries, tables, and layout cues. It can include a settlement PDF's `Schedule A` as a named section/table even when no fields from it are promoted.
2. **Facts** are selective, typed assertions with evidence, scope, and temporal context. Promote a value when it has clear query utility and can be validated reliably. The source text remains evidence, not world truth.
3. **Memory Kinds** should start as a sparse, open hierarchy. Unknown or mixed content is valid; the initial kinds are capabilities for specialized enrichment, not an exhaustive taxonomy.
4. **Retrieval** should combine cheap extraction, typed Fact filters, and full-text search (FTS). Embeddings are a later search projection over useful text, not a replacement for typed Facts or source evidence.
5. **Understanding Runs** should be immutable and versioned. Each Run records the extractor/model provenance, coverage, warnings, partial results, and explicit unknowns before it becomes the active search projection.

This fits the current design context: one user-visible Memory refers to one immutable Blob, while Derived Content and Facts are replaceable by Rebuild ([CONTEXT.md](../../CONTEXT.md); [MVP design snapshot](../MVP.md); [ADR 0003](../adr/0003-understanding-is-rebuildable.md)). The MVP is authoritative; this research and its proposed concrete schemas remain tentative.

## Five document types as a design probe

The examples intentionally omit names, numbers, addresses, account details, and other private identifiers. They show the shape of an interpretation, not a mandatory field list.

| Personal document | Derived Content | Promoted Facts (illustrative) | Retrieval text | Sparse Memory Kind |
|---|---|---|---|---|
| Passport JPEG | Image metadata; OCR text by region; visible text blocks; OCR confidence and unreadable regions | `issue_date`, `expiry_date`, and `nationality` in category `passport`, only when legible and validated; evidence region for each | OCR text plus filename/import metadata; image embedding if enabled later | `identity/passport` |
| Airline ticket PDF | Page text with page numbers; itinerary sections; flight table rows; labels and coordinates where useful | `travel/airline`, `travel/origin`, `travel/destination`, `travel/departure_at`, `travel/arrival_at`, with leg scope and timezone when supported | Page text, route names, flight labels, and normalized date/time strings | `travel/airline-ticket` |
| Title deed PDF | Native/OCR text; parties and property sections; parcel or legal description table; page references | `property/document_date`, `property/jurisdiction`, `property/transaction_type` when validated; parties only if policy and evidence support them | Section text, headings, property terms, and metadata | `property/title-deed` |
| Opinionated investment newsletter Markdown | Source Markdown, headings, paragraphs, links, quoted passages, and optional summary or outline | `publication/published_at` and `publication/author` when explicit; no Facts required for its investment opinions | Full Markdown text, headings, mentioned entities, and linked titles | `publication/newsletter` |
| Employment settlement PDF | Page text; headings; clauses; tables; named `Schedule A` section/table; page and row provenance | `employment/settlement_date`, `employment/parties` only with adequate evidence, `employment/payment_due_at` or amount only with currency, scope, and validation | Clause text, section titles, `Schedule A`, and normalized dates/terms | `legal/employment-settlement` |

The table illustrates a useful asymmetry: all five documents gain searchable Derived Content, while only a small subset of candidate values becomes typed Facts. A missing or uncertain Fact should remain represented as an unknown or warning rather than being filled from a plausible guess.

These examples come from hands-on inspection in this design conversation, not a representative corpus. A native Markdown source may already supply the best searchable text; a redundant extracted-text artifact is optional. A generated summary is useful for orientation but has weaker evidentiary value than source-shaped text or a reproduced table.

The examples also show why value labels alone are insufficient. The ticket had multiple legs and separate check-in and departure times; a date or time must stay attached to its leg and role. The deed had multiple owners and shares. The settlement contained several payment lines and totals with different meanings, plus unresolved alternative wording in a clause. Its extracted `Schedule A` table is useful Derived Content even if none of its amounts are promoted. The newsletter mixed reported events, quotations, forecasts, and the author's opinions; promoting those sentences to unqualified Facts would misstate what the source asserts.

## Derived Content, Facts, and search projections

Derived Content should retain the shape needed to inspect and reprocess a Memory: page/section identity, table structure, text spans, OCR confidence, and extraction warnings. It is descriptive material linked to the Memory, not a separately searchable Memory and not a claim that every extracted string is true.

The authoritative model currently specifies only a Fact's name, category, origin, type, and value. Evidence, context, scope, and temporal semantics remain research questions rather than current Fact fields.

### Keeping the taxonomies small

The possible Facts in arbitrary personal files are effectively unbounded. A fixed catalog will miss domains; a label for every extracted phrase will become unusable. Start with a small set of stable, broadly useful Fact families and add one only when it improves a concrete search or display task, has clear meaning and scope, and can be extracted and checked consistently. Keep document-specific detail in Derived Content. Import path, filename, media type, and filesystem times may be better represented as universal Memory metadata or Import Context than forced into a semantic Fact family; the current glossary and MVP still call some of these provenance Facts, so this placement remains an open domain-model change.

Memory Kind serves recognition and the choice of specialized enrichment. Use a shallow parent and subtype only where the subtype changes processing; leave issuer, brand, jurisdiction, and publication series as separate contextual values. `unknown` is acceptable. A broad `travel/airline-ticket` kind can cover many airlines without one kind per carrier, even if no carrier-specific extractor exists. Media Type remains the Blob's format, not its semantic kind ([CONTEXT.md](../../CONTEXT.md)).

Neither taxonomy needs to cover everything. A Memory with no specialized Kind or no promoted Facts remains searchable through its source text, OCR, and other Derived Content.

FTS is suited to filenames, metadata, section text, OCR output, and other residual query terms. In the current architecture FTS5 is explicitly a rebuildable SQLite projection ([ADR 0007](../adr/0007-go-sqlite-and-filesystem-cas.md)). Embeddings, when added, should be another retrieval projection over selected Derived Content and text, with source spans retained for display and verification. An embedding match can find a semantically related passage; it cannot by itself establish a typed date, amount, identity, or legal status.

A practical query path is:

```text
cheap extraction -> typed Fact filters + FTS terms -> optional embedding candidates
                                                     -> bounded query-time interpretation
```

Query-time interpretation can resolve a residual question against a small candidate set and its evidence. It should not trigger a full reindex merely because a user asked for a new kind of question. Later capability additions can backfill a new projection or specialized Fact family deliberately.

This distinction depends on the search promise. “Find my flight around 15 July” is a best-effort navigation task: date-bearing text and passages can retrieve a likely ticket, then a model can inspect its itinerary. “Find every settlement above a threshold” is an analytic query: claiming completeness requires consistent amount extraction, currency and role semantics, and backfilling across the relevant corpus. Without that coverage, present matches as candidates, not an exhaustive answer. Query-time interpretation cannot establish that unseen files do not match.

There is no universal “extract every fact” prompt. The useful schema depends on the candidate kind, layout, evidence quality, and likely query utility. A specialized extractor should state what it covers and abstain outside that coverage.

## Understanding Run contents

An Understanding Run should be a complete, immutable interpretation candidate. Alongside references to Derived Content and Facts, record:

- extractor, plugin, prompt/schema, and model versions;
- source Blob identity and media type;
- coverage: pages, regions, sections, tables, or character ranges inspected;
- warnings, truncation, OCR quality, unsupported layout, and other limitations;
- partial results and explicit unknowns, including why a candidate Fact was not promoted;
- evidence references for every promoted Fact;
- references to, or build status for, search projections over the Run, including FTS and any embedding model/version; the indexes themselves can be rebuilt independently;
- lifecycle outcome and activation time, with failed attempts kept as logs according to the existing policy.

The Run may complete with warnings when optional enrichment fails. A failed attempt must not replace the active successful Run. This follows ADR 0003's atomic activation rule and the MVP's `InProgress`, `Done`, and `Failed` understanding states. A rebuild or later capability can produce a new Run from the same Blob.

## Local model experiment

The experiment ran locally on an M3 Pro with 36 GB RAM, using an Emirates three-page ticket and three Ollama configurations. Machine swapping was observed, so elapsed times are directional within this experiment and are not a benchmark.

| Model/configuration | Search-card task | Targeted itinerary task | Short passage query |
|---|---:|---:|---:|
| `qwen3:4b` | ~48 s | ~22 s; confused check-in and departure times on both legs | ~4.5 s; correct |
| `gemma4:12b` GGUF | ~100 s | ~56 s; correct times with exact 12/12 evidence snippets | not recorded |
| `gemma4:12b-mlx` | ~40 s | ~29 s; correct times with exact 12/12 evidence snippets | ~5.5 s; correct |

Generic cards for all models showed schema and evidence-copy flaws. The 12B success is one document and does not establish a benchmark. The result does suggest that lightweight local models are viable for high-level classification or search assistance, while deeper Facts need targeted schemas, layout-aware checks, evidence matching, and abstention. It establishes no universal model-size threshold: this is one document, one hardware setup, and a small set of tasks. Runtime claims should remain limited to these observed runs; no broad Ollama speed claim is implied.

The PDF was first converted to page-labelled, layout-preserving text. A generic search-card prompt then asked what the file was and which explicit names, identifiers, dates, and sections might help retrieval. A second, itinerary-specific prompt asked for each leg with evidence; a short-passage query tested answering after candidate selection. This suggests a staged local workflow for unknown PDFs: generic extraction and recognition first, optional kind-specific parsing when justified, and brief query-time reading of retrieved passages. The 4B model's wrong departure times show why plausible JSON alone must not become deep Facts. The 12B model's success on this ticket shows the failure is not inevitable for every local model or task.

The verbatim instruction templates, output schemas, and Ollama settings are preserved in [Exact local-model prompts](understanding-runs-local-model-prompts.md). The expanded prompts also contained personal ticket text, which is omitted from that note.

## Open design decisions

These are questions for the design process, not decisions made by this note:

- Which Fact cardinalities and evidence span format should SQLite store?
- Which Derived Content shapes are stable enough for plugins: page text, regions, tables, or a versioned intermediate representation?
- How should kind confidence and overlapping kinds be represented when a Memory is mixed or unknown?
- Which Fact families warrant deterministic validators, and what is the abstention threshold?
- Which text is included in FTS versus embeddings, and how are projection versions rebuilt and compared?
- How much OCR/layout provenance belongs in the searchable Run versus retained only as inspectable Derived Content?
- What privacy and retention rules apply to evidence snippets and model logs?

## Sources and relationship to current design

- [Project glossary and domain model](../../CONTEXT.md)
- [MVP design snapshot](../MVP.md) — authoritative current snapshot, while this note's proposals remain tentative
- [ADR 0003: Understanding is rebuildable](../adr/0003-understanding-is-rebuildable.md)
- [ADR 0007: Go, SQLite, and filesystem CAS](../adr/0007-go-sqlite-and-filesystem-cas.md) — FTS5 as a rebuildable projection
