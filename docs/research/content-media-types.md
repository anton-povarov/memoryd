# Content and media types in memoryd

Status: historical research; the proposed client-first policy below was superseded by [ADR 0008](../adr/0008-media-type-resolution-is-import-metadata.md)  
Date: 2026-09-16  
Scope: import declarations, server detection, storage metadata, and download representation

## Current memoryd behavior

The multipart import handler opens one `file` part, forwards its filename and filesystem metadata to `vault.Put`, and does not pass either the part's `Content-Type` or a `media_type` form value ([handler](../../server/internal/httpapi/handler.go)). Vault copies the bytes to a temporary Blob under a size limit, hashes them, then detects media type before publishing the content-addressed Blob and inserting the Memory ([Vault](../../server/internal/vault/vault.go)). Its detector recognizes PDF, JPEG, and PNG signatures; for Markdown it requires valid text plus an `.md`/`.markdown` filename or a structural signal. Other content is rejected. The download handler switches over those four stored types to select generated response wrappers ([handler](../../server/internal/httpapi/handler.go)). These are repository observations, not proposed policy.

## What Perkeep does

Perkeep's Blob upload protocol treats a multipart part's `Content-Type` as transport scaffolding: it requires the header for compatibility but says its value is discarded. It recommends `application/octet-stream`. A Blob is addressed by its digest and has no metadata or version at that layer ([Blob upload protocol](https://perkeep.org/doc/protocol/blob-upload)). Perkeep's schema documentation likewise describes the lowest layer as indifferent to Blob contents, with higher layers defining file and other metadata schemas ([schema](https://perkeep.org/doc/schema/)).

Perkeep indexes MIME separately from raw storage. Its Blob sniffer applies content-based recognition, returning an empty MIME type when unknown; schema JSON receives a special `application/json; camliType=...` value ([sniffer source](https://perkeep.org/pkg/index/sniff.go)). For a reconstructed file, the indexer first calls `magic.MIMETypeFromReader`; only when that finds no type does it use `magic.MIMETypeByExtension` on the stored filename. It writes the result into file-index metadata ([indexer source](https://perkeep.org/pkg/index/receive.go)). The regular download path also probes file bytes and falls back to `application/octet-stream` when no type is found; it has an optional explicit `ForceMIME` override ([download source](https://perkeep.org/pkg/server/download.go)).

**Inference:** Perkeep is evidence for keeping byte storage independent of MIME interpretation. It is not precedent for trusting an import-time client declaration: the documented upload path explicitly discards it.

## Relevant format and API constraints

Go's `mime.ParseMediaType` parses a type and optional parameters, lowercases and trims the returned type, and returns an error for malformed parameters. `mime.TypeByExtension` may draw from system MIME databases or files and can return an empty string, so its result is environment-dependent rather than a portable detector ([Go `mime` package](https://pkg.go.dev/mime)).

IANA publishes both a registry of top-level types and registries of registered subtypes. Recognized top levels include `application`, `audio`, `font`, `haptics`, `image`, `message`, `model`, `multipart`, `text`, and `video`; `example` is reserved for examples. MIME syntax validation, checking a top-level category, and requiring a registered subtype are distinct policies ([top-level registry](https://www.iana.org/assignments/top-level-media-types), [media-type registry](https://www.iana.org/assignments/media-types)).

OpenAPI 3.0.3 permits response `content` keys to be a specific media type or media-type range, with the most specific matching key taking precedence. For a multipart binary string property, its default part `Content-Type` is `application/octet-stream`, and `encoding.contentType` can specify another type or wildcard ([OpenAPI 3.0.3, Response Object and Encoding Object](https://spec.openapis.org/oas/v3.0.3)). These specification allowances do not themselves establish what memoryd's generated Go server supports; that requires checking the generator output.

## Decisions from the design discussion

The intended server policy differs from Perkeep's. A client may declare a media type in the multipart `media_type` field; when that field is absent, the file part's `Content-Type` is the declaration. A specific, valid declaration is trusted as metadata **without server detection**. Thus a declared `text/plain` remains `text/plain` even if the bytes look like Markdown. There is no way to tell whether a part header was produced by a standard library or written by hand, so both receive the same validation.

Validate declarations with Go's MIME parser and a recognized top-level category. Reject malformed declarations or unknown top-level categories so the client can retry with a corrected value or `application/octet-stream`. This is deliberately *not* an exact IANA-subtype check: a syntactically valid but unregistered subtype can pass. Normalize the stored type/subtype; the handling of optional MIME parameters remains an implementation detail.

An absent or generic declaration asks the server to detect content. The current PDF/JPEG/PNG signature checks and Markdown text/filename/structure rule are the starting point. If detection succeeds, return the detected type. If it cannot identify the content, preserve a valid generic declaration, or use `application/octet-stream` when no declaration exists. Unknown content should still become a durable Memory with limited understanding; it should not be rejected merely because the detector lacks a format rule. The exact set of generic aliases beyond `application/octet-stream` has not been chosen.

Persist and expose whether the stored media type was client-declared, server-detected, or a fallback. That distinction should remain visible after the import response, including when a Duplicate points to the first Memory. A specific client declaration has no server-detected result to report; the response must not imply that it was verified. Downloads should emit the stored type with attachment disposition and `X-Content-Type-Options: nosniff`, including when the type was client-declared. This makes declaration provenance especially important: a wrong specific declaration can mislead later consumers. Future extractors should inspect bytes before trusting it for processing.

Media type describes a Blob's format. It is distinct from **Memory Kind**, the semantic category used for Specialized Enrichment ([domain glossary](../../CONTEXT.md)). The current [MVP supported-format boundary](../MVP.md) and [OpenAPI import description](../../api/openapi.yaml) still describe a four-format gate. They must be revised if imports of otherwise unknown content become allowed. The proposed policy remains consistent with the [client-owned directory workflow ADR](../adr/0006-clients-own-directory-import-workflows.md): the server still owns validation of each uploaded candidate.

## Where the module seam belongs

The current `Vault.Put` stages and hashes bytes, detects their media type, publishes the Blob, commits the Memory, deduplicates by hash, and completes a stub Understanding Run. The proposed seam leaves Blob publication, size enforcement, content identity, deduplication, and SQLite writes in Vault. Media resolution belongs on the import side and passes its result to Vault as metadata. Understanding decisions also belong outside Vault; Vault should persist their outcomes.

Three implementation shapes were considered:

| Option | Shape | Cost |
| --- | --- | --- |
| Focused refactor (recommended) | A media-resolution function is called by the HTTP import handler; Vault accepts the resolved type and one persisted source field. | Keeps one caller and avoids a pass-through Import wrapper. |
| Smallest immediate change | As above, but leave the stub Understanding completion in Vault temporarily. | The storage seam remains imperfect until understanding is refactored. |
| Full Import module | A coordinator owns resolution, import sequencing, and stub Understanding; retain the exact client declaration as additional provenance. | More interfaces and migration work before there is a real Understanding pipeline. |

The focused option still needs a SQLite migration for provenance, corresponding OpenAPI fields, and a download response that can emit more than the four generated fixed media types. A size preflight at the multipart adapter can preserve a 413 result before media inspection; Vault retains the hard 100 MiB limit. Current behavior and the design choices above should be tested separately so a moved classifier does not accidentally change existing PDF, JPEG, PNG, and Markdown detection.
