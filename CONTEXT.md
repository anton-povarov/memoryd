# Memory Vault

Memory Vault is the domain concerned with preserving things a person may want to find again and describing them well enough to retrieve naturally.

## Language

**Memory**:
A single user-visible thing preserved in the Vault, identified by its content and linked to its Derived Content. A Memory is searched and returned as a whole, even when its contents are processed in smaller pieces internally.
_Avoid_: Item, document, record

**Blob**:
The immutable bytes owned by the Vault and addressed by their content hash. A Memory references exactly one Blob.
_Avoid_: File, source artifact

**Vault**:
The durable collection of Memories owned by memoryd.
_Avoid_: Library, archive, index

**Import Context**:
Information captured from where imported content came from, including its name, available path information, path structure, and filesystem metadata. Import Context produces provenance Facts but is never treated as a live reference on which the Memory depends.
_Avoid_: Source, backing file

**Memory Kind**:
A recognized semantic category of Memory, such as a Tasleem bill or identity document, for which specialized facts may be extracted.
_Avoid_: File type, MIME type, document type

**Derived Content**:
Content produced while understanding a Memory, such as extracted text, image metadata, or a summary. It describes the Memory but is not itself a separately searchable Memory.
_Avoid_: Child memory, sub-document

**Fact**:
A categorized, named, searchable value asserted about a Memory, together with its origin. Facts never overwrite one another merely because they concern similar concepts; distinct origins and meanings coexist.
_Avoid_: Attribute, property, tag

**Fact Namespace**:
The category that distinguishes a Fact's origin or semantic domain, such as `exif`, `bill`, or `passport`.
_Avoid_: Group, prefix

**General Extraction**:
Best-effort understanding applicable to a broad content format, such as extracting text from a PDF or EXIF data from an image.
_Avoid_: Generic parser, fallback parser

**Specialized Enrichment**:
Understanding specific to a detected Memory Kind that adds domain Facts beyond General Extraction.
_Avoid_: Specialized extraction, custom parsing

**Duplicate**:
An import candidate whose content is byte-for-byte identical to an existing Memory. A Duplicate is rejected rather than creating or enriching a Memory.
_Avoid_: Copy, repeated file

**Rebuild**:
Replacement of a Memory's Derived Content and Facts using current extractors and enrichers while preserving its original content and identity.
_Avoid_: Re-import, migration

**Understanding Run**:
An immutable, versioned interpretation of a Memory containing the Derived Content and Facts produced together. A Run is recorded only after successful processing; one Run is active for search while earlier successful Runs remain available for provenance and rollback.
_Avoid_: Parse result, extraction version

**Understanding State**:
The current lifecycle state of a Memory's understanding attempt: `InProgress`, `Done`, or `Failed`. `Done` means a coherent Understanding Run committed, although it may carry explicit warnings from optional enrichment. `Failed` means no usable Run could be committed. A Memory in `Failed` state remains a valid Memory with its Blob and import metadata; it is not an orphan.
_Avoid_: Import state, orphan status

**Orphan Blob**:
Content present in storage that no committed Memory references, normally because an import was interrupted between writing the Blob and recording the Memory.
_Avoid_: Failed Memory, partially understood Memory

**Understanding Plugin**:
A versioned external component that can recognize supported Memories and contribute specialized Facts in an Understanding Run.
_Avoid_: In-process parser, file handler

**Model Task Category**:
A named class of model-assisted work, such as query understanding or hard content extraction, that is independently assigned to a configured model provider.
_Avoid_: Model name, provider name

**Query Plan**:
The visible interpretation of a natural-language query as exact Fact filters and residual full-text terms. Unresolved terms remain mandatory full-text terms rather than being silently discarded.
_Avoid_: Search prompt, relaxed query
