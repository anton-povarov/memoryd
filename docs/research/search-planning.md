# Local multilingual Search Planning

Status: research for the target product in `docs/MVP.md`. Search and Query Plan
types are not present in the active OpenAPI contract or current server slice.

Status: research note, not product specification  
Date: 2026-09-20  
Scope: practical local approaches for converting a multilingual natural-language search query into memoryd's visible Query Plan. The target fields are residual full-text terms, Media Type, Memory Kind, a date range, and an optional location. This note compares deterministic parsers, compact generative models, multilingual NER, and embedding retrieval. It does not claim an accuracy level that has not been measured on a memoryd fixture corpus.

## Repository constraints and the scope conflict

The domain vocabulary is important here. A **Media Type** is a format label on a Blob, used for transfer and Document Understanding; it is explicitly distinct from a **Memory Kind**, which is a semantic category such as an AC bill or identity document ([`CONTEXT.md`](../../CONTEXT.md), “Media Type” and “Memory Kind”). The import boundary reinforces this distinction: Media Type is detected from Blob bytes and may remain generic, while storage is format-independent ([ADR 0008](../adr/0008-media-type-resolution-is-handled-by-memoryd.md)). A query saying “PDF bills” therefore contains two different possible constraints: a format constraint and a semantic kind constraint. A model must not collapse them into one “document type” field.

The target Query Plan is the visible interpretation of a query as exact Fact filters and residual full-text terms; unresolved terms remain mandatory terms ([`CONTEXT.md`](../../CONTEXT.md), “Query Plan”). A prior proposed contract used this shape:

```yaml
QueryPlan:
  fact_filters: [FactFilter]
  full_text_terms: [string]
  explanation: string
```

In that proposal, `FactFilter` carried a category, name, comparison operator,
and typed value, and the plan was displayed before executing exact Fact filters
plus mandatory residual FTS terms. This means future Search Planning is not
merely an internal ranking hint: dropping a phrase or inventing a filter changes
the user-visible search semantics.

The authoritative MVP adds four constraints:

- the plan is visible, and known Fact constraints become exact filters;
- remaining or unresolved terms are mandatory full-text terms, and query relaxation is out of scope;
- initial search is typed Fact filtering plus SQLite/FTS5 over filenames, metadata, and Derived Content; embeddings and relevance ranking are deferred;
- parsers may declare kind-specific date fields and semantics, such as preferring a Tasleem billing period for a year query rather than treating every date as equivalent ([`docs/MVP.md`](../MVP.md), “Search”).

The MVP also defers cross-language retrieval ([`docs/MVP.md`](../MVP.md), “Explicit MVP boundaries”). This creates a scope conflict with a broader multilingual/cross-language goal. A multilingual parser can understand a Russian, Arabic, or French query and emit a normalized date or MIME filter, but FTS still cannot find an English-only stored phrase merely because its meaning is equivalent. Conversely, adding embeddings later could improve cross-language recall but would not make an embedding match an exact Fact constraint. The MVP can therefore support multilingual query *parsing* without promising cross-language *retrieval*. The latter needs a deliberate future projection and evaluation track.

Model selection belongs to the `search_planning` Model Task Category, which is independently routed to a configured provider; ADR 0005 names Ollama as the intended provider and disallows automatic provider fallback ([ADR 0005](../adr/0005-models-are-routed-by-task-category.md)). The currently installed `embeddinggemma` is not a chat/query parser according to the MVP, so it should not be treated as a substitute for the configured model.

## Candidate approaches

### Deterministic extraction

Deterministic extraction is the safest first layer for fields with a finite vocabulary or normalization rules:

- Media Type aliases can map `pdf`, `portable document`, `jpeg`, `photo`, and similar terms to a constrained MIME allowlist. This is Search Planning, not Media Type detection; the authoritative stored Media Type still comes from import metadata.
- Memory Kind aliases can map known terms such as `passport`, `Tasleem bill`, `airline ticket`, or `newsletter` to the currently supported semantic-kind vocabulary. Unknown terms remain text instead of creating arbitrary Fact categories.
- Numeric and ISO-like dates, years, explicit ranges, and locale-specific formats can be handled before a model call. A locale or reference time must be recorded because `03/04/2026` is ambiguous and “last month” is relative.

For natural-language dates, [Microsoft Recognizers-Text](https://github.com/microsoft/Recognizers-Text) is an MIT-licensed, local recognition/resolution library for numbers, units, and date/time entities. Its project documentation lists full support for Chinese, English, French, Spanish, Portuguese, German, Italian, Turkish, Hindi, and Dutch, with partial support for Japanese, Korean, Arabic, and Swedish. It has explicit DateTime recognition and resolution APIs, including date ranges and a reference time, but its primary implementations are .NET, JavaScript, Java, and Python rather than Go. It could run behind a dedicated local subprocess boundary; it is not an Understanding Plugin, because it interprets queries rather than Memories.

The BSD-3 licensed [dateparser](https://github.com/scrapinghub/dateparser) is another practical plugin candidate. Its first-party documentation claims support for more than 200 language locales, language autodetection, absolute and relative dates, time zones, non-Gregorian calendars, and span expressions such as “past month” or “last week.” The breadth is attractive, but autodetection and ambiguous numeric dates still require a policy: when the locale cannot be established, abstain from a Fact filter and retain the source text.

[HeidelTime](https://github.com/HeidelTime/heideltime) is a multilingual temporal tagger that normalizes expressions to TIMEX3. Its repository says it has hand-crafted resources for 13 languages and automatically generated resources for 200-plus additional languages, with lower quality for the generated resources. It is GPL-3.0. That license and its Java/UIMA-oriented deployment make it a less convenient MVP dependency, though it is a useful comparison or separately packaged plugin.

Deterministic extraction alone will not robustly classify free-form semantic kinds or arbitrary locations in every language. It should be the authority for exact normalization, not be forced to cover every phrase.

### Compact generative structured extraction

A small instruction model can classify a query, choose among supported fields, and return a structured candidate when deterministic rules do not recognize the wording. The best current shortlist is Qwen3:

- [Qwen3-1.7B](https://huggingface.co/Qwen/Qwen3-1.7B) is Apache-2.0. The [Qwen3 technical report](https://arxiv.org/abs/2505.09388) states that Qwen3 expands multilingual support from Qwen2.5's 29 languages to 119 languages and dialects. The official Ollama catalogue lists the quantized `qwen3:1.7b` at about 1.4 GB with a 40K context window ([Ollama tags](https://ollama.com/library/qwen3/tags)). This is a reasonable default to benchmark for query extraction: small enough for local use, but not the smallest available model.
- [Qwen3-0.6B](https://huggingface.co/Qwen/Qwen3-0.6B) is also Apache-2.0. Ollama lists its Q4 variant at about 523 MB and a 40K context window ([Ollama model page](https://ollama.com/library/qwen3%3A0.6b)). It is a useful lightweight benchmark candidate for classification, but no accuracy claim should be made until the query fixture suite measures it. Selecting it would be an explicit task-category configuration, not an automatic fallback.
- [Qwen2.5-1.5B-Instruct](https://huggingface.co/Qwen/Qwen2.5-1.5B-Instruct) is an Apache-2.0 instruction model with 1.54B parameters. Its model card explicitly reports improved structured/JSON output, 32K context, and support for more than 29 languages. It may be a useful compatibility baseline, but Qwen3 has the stronger stated language-coverage case.

Ollama's first-party [structured-output documentation](https://github.com/ollama/ollama/blob/main/docs/capabilities/structured-outputs.mdx) supports passing a JSON Schema in the local API's `format` field and validating the returned JSON with a schema library. The documentation recommends low temperature and also putting the structure in the prompt. Schema-constrained decoding makes malformed JSON less likely; it does not prove that the model selected the correct date role, location, or Memory Kind. `format: json` is therefore a transport/shape guarantee, not a semantic accuracy guarantee.

The model should only emit candidates from an allowlist of supported Fact categories, operators, Media Types, and Memory Kinds. It should return exact source spans rather than a rewritten “full text” phrase. The Go server can then decide which spans were accepted and construct the public plan.

### Multilingual NER for location and dates

[GLiNER multilingual v2.1](https://huggingface.co/urchade/gliner_multi-v2.1) is an Apache-2.0 zero-shot NER model. The [project documentation](https://github.com/urchade/GLiNER) describes CPU/consumer-hardware deployment, arbitrary entity labels, ONNX export, and multilingual models; its model table lists the multilingual v2.1 model at 209M parameters. The Hugging Face repository currently reports about 2.31 GB of files, so the storage footprint is materially larger than a small date parser or a Qwen3 Q4 model.

GLiNER could identify a `location` or `date` span without making the generative model responsible for every entity. However, a location mention is not automatically a query constraint: “photos in Dubai” likely means a location Fact, while “Dubai airport” may be a full-text phrase or an itinerary endpoint. NER should therefore be optional enrichment, followed by semantic validation and date-role logic. It is not required for the first MVP.

### Embedding retrieval

Embeddings solve a different problem from Query Plan extraction. They can add semantic or cross-language candidate recall, but they do not establish an exact typed Fact and cannot replace mandatory FTS terms in the current contract.

- [multilingual-e5-small](https://huggingface.co/intfloat/multilingual-e5-small) is MIT-licensed and tagged for 94 languages. Its published configuration has 384-dimensional embeddings, 12 layers, and a 512-token maximum position length. It is the most attractive lightweight future candidate for local semantic retrieval.
- [BGE-M3](https://huggingface.co/BAAI/bge-m3) is MIT-licensed, supports more than 100 languages, produces 1024-dimensional vectors, accepts up to 8192 tokens, and supports dense, sparse, and multi-vector retrieval. Its model repository is about 4.59 GB. The model card recommends hybrid retrieval plus reranking and explicitly notes that BM25 remains competitive on some long-document cases. It is a stronger, heavier future projection rather than an MVP query parser.
- Google's [LaBSE](https://research.google/blog/language-agnostic-bert-sentence-embedding/) is an older cross-lingual sentence-embedding baseline supporting 109 languages. It is useful as a comparison point, but E5 is a more practical first experiment for this project.

On macOS, [MLX](https://mlx-framework.org/) is Apple's local Apple-Silicon framework: its first-party site describes unified-memory design, CPU/GPU execution, and Python, C++, and Swift bindings. An official [Qwen3-1.7B MLX checkpoint](https://huggingface.co/Qwen/Qwen3-1.7B-MLX-bf16) exists. This makes direct MLX deployment viable, but the MVP's provider route is Ollama, so Ollama Q4 should be the initial deployment target. A later benchmark can compare Ollama/GGUF and MLX on the same fixtures and hardware.

## Conservative hybrid pipeline

The recommended design is a deterministic pass, a bounded model pass, and a conservative merge:

1. Preserve the original query and tokenize it without losing offsets. Run exact aliases, MIME normalization, numeric/ISO date rules, and the selected deterministic date parser. Detect only supported Memory Kinds and Fact fields.
2. Send the original query plus the deterministic candidates and the supported vocabulary to Ollama under the `search_planning` task category. Ask for candidates, exact source spans, locale assumptions, date precision, date role, confidence, and an explicit abstention when the wording is ambiguous. Use a JSON Schema and temperature 0.
3. Merge by authority: deterministic values win when unambiguous; the model may fill an unrecognized semantic alias or location candidate, but it may not override a validated date or invent an unsupported field. Require exact substring spans, valid offsets, allowlisted values, and `from <= to` for ranges.
4. Remove only accepted constraint spans from the residual text. Preserve all other text, including unresolved aliases, unknown locations, relationship phrases, and model-abstained spans. Do not translate or paraphrase the residual terms.
5. Map accepted candidates to the existing `FactFilter` array. The category and name choices remain an implementation decision; the public shape does not need a new model-specific field. Emit residual phrases in `full_text_terms` and generate `explanation` from the accepted filters and retained terms.

An internal extraction object can be richer than the public API:

```json
{
  "query": "show PDF passport photos from last summer in Dubai",
  "candidates": {
    "media_type": [{"value": "application/pdf", "span": [5, 8], "confidence": 0.99}],
    "memory_kind": [{"value": "identity/passport", "span": [9, 17], "confidence": 0.91}],
    "date_range": [{"from": "2025-06-01", "to": "2025-08-31", "role": "event", "span": [25, 36], "confidence": 0.84}],
    "location": [{"value": "Dubai", "span": [40, 45], "confidence": 0.78}]
  },
  "residual_spans": [{"text": "photos", "span": [18, 24], "reason": "unresolved full text"}],
  "abstentions": [],
  "warnings": []
}
```

The fields, confidence values, and spans are internal evidence, not necessarily additions to the OpenAPI response. The public plan remains the contract the user sees. Confidence must not be presented as calibrated probability until calibration data exists; it is a decision signal for acceptance versus abstention.

## Failure policy

- If Ollama is unavailable, do not silently route to another model provider. Use the deterministic result only when it is a valid plan; otherwise return the existing 422-style interpretation error with an actionable warning.
- If JSON/schema validation fails, discard only the invalid model candidates and retain the original text. Never execute a partially accepted model plan that silently lost terms.
- If a date is locale-ambiguous, has an unknown reference time, or has an unresolved role, keep it as full text rather than creating an exact Fact filter.
- If a location is recognized but its semantics are unclear, retain it as full text unless the supported query vocabulary explicitly defines location filtering.
- If a model suggests an unsupported Memory Kind, Fact category, Media Type, or operator, abstain from that candidate.
- Contradictory accepted constraints (for example, an inverted range) should be a visible interpretation error, not an automatically relaxed query.

These policies preserve the MVP promises: no silent relaxation, no invented facts, and a plan that explains exactly what will execute.

## Evaluation plan

Before choosing a default model, build a versioned query fixture corpus from the MVP examples and synthetic variants. Each fixture should contain the original query, expected public Query Plan, accepted exact spans, date role/precision, expected abstentions, and language/locale metadata. Include:

- English, Russian, Arabic, French, and at least the other languages relevant to the intended vault, with mixed-script and code-switched queries;
- MIME aliases, semantic-kind aliases, filenames, explicit names, unknown kinds, and phrases that must remain full text;
- absolute dates, years, ranges, relative expressions, timezone-bearing dates, non-Gregorian examples if supported, and ambiguous numeric formats;
- location names, airports, addresses, and location-like phrases that are not location filters;
- kind-specific semantics such as a Tasleem billing year versus issue date;
- relationship aliases such as “my wife,” which the MVP deliberately treats as literal text;
- malformed model JSON, unsupported schema values, model timeouts, and contradictory constraints.

Measure field-level precision/recall/F1, exact-plan match, residual-term recall, date-role accuracy, abstention rate, invalid-output rate, cold/warm latency, peak memory, and model download size. Compare deterministic-only, model-only, and hybrid variants. Run each model with pinned weights, quantization, runtime version, and hardware. Report confidence calibration only after collecting enough labeled examples. Keep cross-language retrieval as a separate experiment because it is outside the current MVP contract; test it only after an embedding projection is designed.

## Open decisions

1. Which Fact category and name represent stored Media Type in `FactFilter`, and which represent Memory Kind? ADR 0008 makes the former import metadata, while `CONTEXT.md` makes the latter semantic; the query parser must preserve that distinction.
2. What does `location` mean in the product: a document's depicted place, an event destination, an address, an origin/destination pair, or a generic text constraint? A single string is insufficient for all these roles.
3. Which date roles are supported per Memory Kind, and how are ranges represented when a query says only a year or season? Tasleem billing period is already called out as a distinct semantic.
4. Which locales and reference timezone are configured for relative dates? A deterministic parser must not turn “last month” into a different range on different machines.
5. Should confidence and evidence spans be retained in processing logs or a private intermediate result for plan debugging? They are valuable for evaluation but need not become public API fields.
6. When cross-language retrieval eventually arrives, should it supplement FTS, replace only residual-term recall, or participate in ranking? It must remain separate from exact Fact filtering and preserve source evidence for every result.

The immediate decision is therefore modest: benchmark Qwen3-1.7B Q4 through Ollama behind a deterministic parser, using the existing Query Plan shape and strict abstention. Do not commit to embeddings, GLiNER, a new public schema, or cross-language retrieval until the fixture results and location/date semantics justify them.
