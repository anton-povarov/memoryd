# Token-efficient PDF ingestion for memoryd

Status: research note  
Date: 2026-09-14  
Scope: PDF ingestion and cloud-model escalation under strict per-Memory budgets

## Executive answer

memoryd can reproduce the useful *shape* of the Codex PDF workflow using public, ordinary components: inspect the PDF locally, extract its text page by page, identify pages that need visual interpretation, and render only those pages for a vision model. There is no public evidence that Codex has a proprietary mechanism that makes an arbitrary 2 MiB PDF intrinsically cheap in model tokens. The documented Codex PDF skill instead calls for local text extraction with `pdfplumber` or `pypdf` and local page rendering with Poppler before visual inspection ([OpenAI PDF skill](https://github.com/openai/skills/blob/main/skills/.curated/pdf/SKILL.md)).

This differs materially from sending a PDF directly as an OpenAI `input_file`. The Responses API documents that, for a PDF, it extracts both the text and page images and sends both to a vision-capable model. OpenAI explicitly warns that both contribute to token use. `detail: low` reduces the visual component, but the extracted text is still included ([OpenAI file-input guide](https://developers.openai.com/api/docs/guides/file-inputs)). Therefore, direct whole-PDF upload is not the best default when memoryd must enforce a 50,000-character ceiling and predictable image cost.

Recommended MVP design:

1. Keep the immutable original PDF in local CAS.
2. Perform metadata inspection, native-text extraction, known-parser probing, page scoring, OCR, and page rendering locally under separate resource limits.
3. Send only a bounded derivative: selected text and, only when required, selected page images.
4. Enforce the 2 MiB, 30-page, 50,000-character, and 2-call limits cumulatively across one Understanding Run, before any network request.
5. Do not reject a large original merely because it is larger than 2 MiB. Gate the bytes actually disclosed to the cloud, while separately limiting local CPU, memory, decompression, and page traversal.

## What is documented, and what is inference

### Documented OpenAI behavior

- A PDF passed as an API `input_file` is processed as extracted text plus page images on vision-capable models. For Responses requests, `detail` can be `auto`, `low`, or `high`; it changes page-image processing only, not inclusion of extracted text ([file-input processing and PDF detail](https://developers.openai.com/api/docs/guides/file-inputs#how-it-works)).
- OpenAI warns that PDF text and page images both consume context. The documented platform ceiling is 50 MB per file and 50 MB combined per request, but that is a platform maximum, not a sensible memoryd budget ([file-input usage considerations](https://developers.openai.com/api/docs/guides/file-inputs#usage-considerations)).
- For image inputs, the API converts images to billable tokens. Current documentation exposes model-specific patch/tile formulas and says that low detail constrains current GPT-5.6-family image inputs to 512 by 512 pixels. The exact token rule is model-specific and can change, so memoryd must bind an estimator to the configured model rather than hard-code one universal conversion ([OpenAI vision sizing and cost rules](https://developers.openai.com/api/docs/guides/images-vision#calculating-costs)).
- The Responses API exposes an input-token-count endpoint. It can be used as a provider-side preflight where the selected authentication/product surface supports it ([Responses input-token count API](https://developers.openai.com/api/reference/resources/responses/subresources/input_tokens/methods/count)).

### Documented Codex App Server boundary

Codex App Server is the supported embedding surface for a product that wants to use a user's ChatGPT subscription. It supports managed ChatGPT OAuth through both browser and device-code flows; Codex persists and refreshes the tokens, and the documentation shows `plus` as a possible resulting plan type. It also supports structured turn output through `outputSchema` and publishes token-usage updates after or during work ([Codex App Server](https://learn.chatgpt.com/docs/app-server)).

Two constraints matter for memoryd:

- App Server's documented turn inputs are `text`, URL `image`, and `localImage`. PDF is not a documented direct turn-input type.
- The App Server docs expose usage notifications and ChatGPT rate-limit inspection, but do not document an exact preflight token-count method for a prospective turn.

Therefore, a ChatGPT-OAuth-backed `codex_app_server` adapter should receive memoryd's already-bounded text and locally rendered page images. The OAuth token managed by App Server must not be assumed to authorize arbitrary Responses API calls; official documentation does not grant that contract. Keep an API-key-backed `openai_responses` adapter separate if exact `/responses/input_tokens` preflight or native `input_file` support is later desired.

### Documented Codex workflow

The public OpenAI PDF skill recommends two local paths:

- `pdfplumber` or `pypdf` for text extraction and quick checks;
- Poppler's `pdftoppm` to render pages when layout or visuals matter.

It also warns not to rely on extracted text for layout fidelity ([OpenAI PDF skill](https://github.com/openai/skills/blob/main/skills/.curated/pdf/SKILL.md)). This is evidence for a staged local-tool workflow. It is **not** documentation of Codex's proprietary attachment transport, internal token accounting, or any hidden PDF compression.

### Documented local-library behavior

- `pypdf` can extract normal or layout-oriented text, but it is not OCR software and cannot extract text from images. Its documentation recommends OCR for image-only scanned pages and explains that PDFs lack a semantic layer for concepts such as headers, tables, and paragraphs ([pypdf text extraction](https://pypdf.readthedocs.io/en/6.18.1/user/extract-text.html)).
- `pdfplumber` provides page-scoped text, words with coordinates, object metadata, table extraction, cropping, and page-range selection. Its own README says it works best on machine-generated rather than scanned PDFs ([pdfplumber README](https://github.com/jsvine/pdfplumber/blob/stable/README.md)).
- PyMuPDF can OCR complete pages or image regions through Tesseract. Its documentation recommends testing whether OCR is needed first (for example, no text or a page covered by an image), and says OCR is roughly one thousand times slower than normal text extraction; it recommends caching the resulting `TextPage` for reuse ([PyMuPDF OCR documentation](https://pymupdf.readthedocs.io/en/latest/recipes-ocr.html)).
- A small compressed PDF is not necessarily cheap to process. `pypdf` documents that text extraction must parse a page's whole content stream and recommends inspecting decompressed content-stream size to avoid excessive memory use ([pypdf text-extraction resource warning](https://pypdf.readthedocs.io/en/5.9.0/user/extract-text.html)). Current `pypdf` also exposes safety limits for recovery and stream processing ([pypdf security configuration](https://pypdf.readthedocs.io/en/6.18.0/user/security.html)).

### Inferences and design recommendations

Everything below is a proposed memoryd design, not a claim about undocumented Codex internals:

- Codex's apparent efficiency is plausibly explained by local extraction and targeted visual inspection, because that is the public skill's prescribed workflow.
- Constructing explicit text and image inputs gives memoryd better control than direct PDF input over disclosed bytes, selected pages, extracted characters, and estimated model tokens.
- The source Blob's byte size should be a local resource signal, not the cloud-disclosure limit. The actual derivative sent to a provider should be the object measured against the cloud budget.

## Proposed local-first PDF pipeline

### Stage 0: immutable source and cheap inspection

Store the original bytes once in CAS and attach the Memory to that Blob. Before extraction, record:

- cryptographic content hash and byte length;
- MIME detection result, filename, import path, and filesystem timestamps;
- PDF page count, encryption state, document metadata, and parser warnings;
- tool name/version for every subsequent artifact.

Do not make a cloud call in this stage. Apply local time, memory, decompressed-stream, and subprocess-output limits. An encrypted PDF without a supplied password should become a successful import with limited understanding, not an unbounded retry loop.

### Stage 1: native text, one page at a time

Extract page text locally before OCR. Preserve page boundaries and basic positional information. For each page, retain small diagnostic signals such as:

- extracted character and word counts;
- text-to-page-area density;
- whether the page is substantially image-covered;
- suspicious replacement-character or decoding rates;
- likely table/form density;
- keyword hits relevant to detected or candidate Memory kinds.

Stop or truncate according to local safety limits, but do not confuse that truncation with the cloud's 50,000-character budget. The full local extracted text may remain useful for local full-text search even when only a subset can be disclosed to a cloud model.

### Stage 2: deterministic and specialized local understanding

Run file metadata extraction and known specialized plugin probes before generic cloud escalation. A high-confidence Tasleem plugin, for example, may produce the needed Facts without any cloud call. Specialized enrichment should augment, not erase, generic extraction.

### Stage 3: page triage

Build a deterministic page-selection plan. The plan should be explainable and reproducible from stored signals. A useful initial policy is:

1. always consider the first page;
2. include pages with candidate-kind keywords or dates/amounts relevant to the parser schema;
3. include text-poor, image-heavy pages when visual content may contain the missing facts;
4. include representative pages from repeated layouts rather than every copy;
5. reserve capacity for the final page when signatures, totals, or appendices are plausible;
6. deduplicate repeated pages by perceptual image hash and normalized-text hash.

The selected set must contain at most 30 distinct source pages per Understanding Run. Store both selected and omitted page numbers with reason codes.

### Stage 4: cloud call 1, text first

If deterministic and specialized extraction are insufficient, call the configured `hard_content_extraction` provider with:

- document/file metadata useful to classification;
- a page-labelled text selection capped at 50,000 Unicode characters across the entire run;
- an explicit structured-output schema;
- truncation markers and omitted-page metadata.

Select text by page and semantic utility, not simply the first 50,000 characters. This call should classify the Memory, extract Facts supported by evidence, identify uncertainty, and request page numbers for visual follow-up if needed.

Text-only call 1 is the predictable default. It avoids paying for page images when native extraction already answers the task.

### Stage 5: local OCR or rendering escalation

If pages have no usable text, OCR only those pages locally and cache the OCR result as a derived artifact. OCR text can either complete local extraction or consume part of the same 50,000-character cloud disclosure budget.

If layout, handwriting, photos, seals, charts, or ambiguous fields still require vision, render only selected pages. Downscale and encode each image to fit the remaining byte budget. Prefer direct image inputs at an explicit detail setting; their dimensions and model-specific token estimate are more visible than the implicit page images produced by a PDF `input_file`.

### Stage 6: optional cloud call 2

Use the second and final call only for evidence that genuinely requires vision or for a narrowly scoped correction. Send:

- only the selected page images;
- page identifiers and the first call's unresolved questions;
- the minimum text needed to interpret those images;
- the same structured-output contract.

Merge the result into the candidate Understanding Run locally. Atomically activate the Run only after all required stages succeed.

## Budget contract

Treat the limits as cumulative per Memory per Understanding Run, not independently per request:

| Budget | MVP limit | Measurement |
|---|---:|---|
| Cloud-derived document bytes | 2 MiB | Sum of decoded bytes of all document-derived text/files/images sent across calls 1 and 2. Also enforce a separate HTTP request-body ceiling to account for JSON and base64 overhead. |
| Source pages disclosed | 30 | Count distinct original page numbers represented by text, OCR text, cropped regions, images, or derivative PDFs sent to the provider. |
| Extracted characters disclosed | 50,000 | Count Unicode scalar values after normalization in all document-derived text sent across both calls; do not count static prompts/schema. |
| Model calls | 2 | Count generation/vision requests for this category and Run. A provider token-count preflight, if used, should be logged separately and must not be allowed to trigger generation. |

This cumulative interpretation is stricter and easier to explain than applying the limits separately to each call. Maintain a `CloudBudgetLedger` before transmission and reject payload construction when any next addition would exceed a limit. Record estimates before the call and provider-reported token usage afterward.

The 2 MiB limit controls disclosure and transfer volume, but it does not by itself cap token use: 2 MiB of dense text can be a very large prompt, and image token cost depends on dimensions/detail rather than compressed JPEG/PNG bytes alone. Enforce an additional configurable `max_estimated_input_tokens` once a provider/model is chosen.

The complete provider request envelope and static instructions also consume tokens. For the direct Responses API, preflight the complete prospective request with `POST /v1/responses/input_tokens`, which accepts multimodal inputs ([OpenAI token-counting guide](https://developers.openai.com/api/docs/guides/token-counting)). For Codex App Server, no equivalent prospective-count method is documented; enforce conservative local limits and record `thread/tokenUsage/updated` afterward.

## Should a 200 MB original be cloud-ineligible?

Not automatically.

The immutable source and the cloud payload are different artifacts. A 200 MB scanned PDF might locally yield one relevant 300 kB page image; a 1 MB highly compressed PDF might expand into a huge content stream. Original byte size is therefore a poor proxy for either disclosure or compute cost.

Recommended configuration separates three concerns:

```text
cloud.max_disclosed_bytes_per_run       = 2 MiB
cloud.max_disclosed_pages_per_run       = 30
cloud.max_disclosed_chars_per_run       = 50_000
cloud.max_model_calls_per_run            = 2

local.max_source_bytes_to_parse          = configurable safety limit
local.max_pages_to_inspect               = configurable safety limit
local.max_decompressed_stream_bytes      = configurable safety limit
local.max_cpu_time / max_wall_time        = configurable safety limit

privacy.max_source_bytes_eligible_for_cloud_derivation = unset by default
```

The optional privacy rule is separate because some users may reasonably define “do not use cloud for files larger than X” even when only a small derivative would leave the machine. The default should permit a locally produced bounded derivative, show exactly what will be sent in logs/UI, and never upload the original unless that exact original itself fits and the chosen route explicitly calls for it.

## Why direct PDF input should be an opt-in route

Direct PDF input is convenient and preserves visual context, but it weakens memoryd's strict controls:

- the API includes extracted text even at low image detail;
- the API documentation does not expose a 50,000-character extraction cutoff for PDF input;
- every page image is included by the documented PDF processing path;
- compressed byte size does not predict extracted-text or image-token cost.

For a short, already-small PDF, an adapter may offer a direct-PDF route only when all budgets can be conservatively proven before upload. The default strict route should remain locally selected text plus explicit page images.

## MVP implementation boundary

The first version does not need a generalized document-understanding framework. A PDF extractor process can emit:

```json
{
  "page_count": 8,
  "pages": [
    {
      "number": 1,
      "native_text": "...",
      "text_quality": 0.98,
      "image_coverage": 0.12,
      "ocr_needed": false
    }
  ],
  "warnings": []
}
```

A separate payload planner consumes this result and the specialized-plugin results, chooses pages/text, renders or OCRs only as necessary, and produces a manifest before invoking a model adapter. The model adapter must accept an already-bounded payload; it must not be allowed to reopen the CAS Blob and silently upload it.

That separation is the key architectural safeguard:

```text
CAS Blob -> local extractor -> local/specialized facts -> payload planner
                                                        |
                                                        v
                                              bounded manifest + ledger
                                                        |
                                                        v
                                                  model adapter
```

## Decisions recommended for the design discussion

1. Define 2 MiB, 30 pages, 50,000 characters, and 2 calls as cumulative cloud-disclosure budgets per Understanding Run.
2. Add a separate estimated-input-token ceiling per model configuration; bytes are not a token budget.
3. Permit large source Blobs to produce small cloud-bound derivatives, subject to independent local resource limits.
4. Default to text-first cloud extraction, with targeted vision only as call 2.
5. Keep direct PDF `input_file` support opt-in because its extracted-text volume is not locally controllable.
6. Store the page-selection manifest, payload hashes, tool versions, reason codes, limits, and usage with the Understanding Run for auditability and reproducible rebuilds.

## Sources

- [OpenAI API: File inputs](https://developers.openai.com/api/docs/guides/file-inputs)
- [OpenAI API: Images and vision](https://developers.openai.com/api/docs/guides/images-vision)
- [OpenAI API reference: Count response input tokens](https://developers.openai.com/api/reference/resources/responses/subresources/input_tokens/methods/count)
- [OpenAI-maintained PDF skill](https://github.com/openai/skills/blob/main/skills/.curated/pdf/SKILL.md)
- [Codex App Server](https://learn.chatgpt.com/docs/app-server)
- [pypdf: Extract text](https://pypdf.readthedocs.io/en/6.18.1/user/extract-text.html)
- [pypdf: Security](https://pypdf.readthedocs.io/en/6.18.0/user/security.html)
- [pdfplumber README](https://github.com/jsvine/pdfplumber/blob/stable/README.md)
- [PyMuPDF: OCR](https://pymupdf.readthedocs.io/en/latest/recipes-ocr.html)
