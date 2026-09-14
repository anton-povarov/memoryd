# Future area: document and media understanding

Status: deliberately deferred beyond the first memoryd MVP.

Understanding heterogeneous files is expected to become a large subsystem. The MVP should leave seams for it without attempting to solve it comprehensively.

## Concerns already identified

- Separate immutable source content from rebuildable interpretations and search projections.
- Keep format mechanics separate from semantic Memory Kinds: PDF is a format; Tasleem bill is a kind.
- Run filesystem metadata extraction, General Extraction, and Specialized Enrichment as distinct layers.
- Detect native text quality before choosing OCR or vision.
- Preserve page boundaries, layout clues, tables, image regions, and evidence when needed.
- Handle scanned PDFs, mixed native/scanned pages, encrypted files, malformed files, decompression bombs, huge page counts, and expensive OCR.
- Triage pages locally and render or OCR only what is useful before cloud escalation.
- Distinguish source-file size, decompressed local work, disclosed cloud bytes, model tokens, page/image detail, and model call counts.
- Direct OpenAI PDF input includes extracted text and page images; it is therefore less predictable than explicitly selected text and rendered pages.
- Codex App Server documents text and image inputs rather than PDF turn inputs. A Codex worker can inspect local files with tools, but this behavior is agentic and must be evaluated empirically.
- Constrain the Codex worker against prompt injection and unnecessary filesystem, network, or write access.
- Version plugins, models, prompts, schemas, OCR engines, and extraction tools so a Rebuild is attributable and reproducible enough to debug.
- Keep prior successful Understanding Runs for provenance while making only one Run active in search.
- Preserve conflicting Facts with their semantic meaning and provenance instead of flattening them into a single generic date or value.
- Define how future user corrections coexist with extractor-produced Facts.
- Support multilingual OCR, extraction, query expansion, and retrieval.
- Add embeddings only after full-text and structured retrieval establish a measurable baseline.
- Evaluate extraction accuracy separately from retrieval quality, using representative personal-document fixtures and gold queries.
- Decide whether future emails, archives, and compound documents remain one Memory or expose linked child Memories; the MVP returns one imported file as one Memory.
- Establish cloud disclosure logs, redaction, retention, payload limits, and token/cost controls based on observed usage.

## Candidate future pipeline

```text
Blob
  -> local format inspection
  -> native text and structure
  -> specialized plugin probing/enrichment
  -> quality assessment and page/region triage
  -> selective OCR/rendering
  -> bounded model payload
  -> versioned Understanding Run
  -> rebuildable search projections
```

This is a research direction, not an accepted MVP implementation plan. The detailed findings that motivated it are recorded in [Token-efficient PDF ingestion](../research/pdf-ingestion-efficiency.md).
