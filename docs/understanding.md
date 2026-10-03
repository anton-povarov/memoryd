# Document Understanding

Document Understanding interprets a stored Memory and produces Derived Content for inspection and search. Plugins perform extraction and interpretation; memoryd controls execution and publishes successful results as immutable Understanding Runs.

## Responsibility and configuration

Memoryd owns configuration, scheduling, plugin selection, validation, and durable results.

One configured plugin handles each input Media Type. Separate processes support different languages and contain crashes. Commands run directly; relative executable paths resolve from the server's working directory, and bare names through `PATH`.

Plugins must use runtime settings supplied by memoryd, rather than load independent configuration or choose fallback models. Model selection comes from `models.document_understanding`; model-free plugins need none.

## Plugin interface

Each attempt launches one plugin process:

- **stdin:** one JSON request containing source content, Import Context, user guidance, and runtime settings supplied by memoryd.
- **stdout:** one JSON result containing artifacts and optional warnings/reporting.
- **stderr:** diagnostics and optional progress records.

The server validates responses and stores text artifacts with their content type, provenance, and scope. Structured values remain JSON artifacts; typed Fact management is deferred.

The [plugin protocol](understanding-plugin-v2.md) defines the exact exchange and links its response schema.

## Runs, storage, and activation

Attempts represent queued, running, or terminal work. Successful attempts create immutable Runs; one Run is active per Memory. Warnings and incomplete coverage are valid results. Unsupported Media Types produce empty Runs with warnings. Process errors or invalid responses produce failed attempts with diagnostics.

The original Blob stays unchanged. Derived bytes occupy a separate CAS namespace; SQLite stores terminal attempts, Run metadata, and active selection. After artifact publication, Run activation and [search updates](search.md) commit together. Failed Rebuilds preserve the previous active Run; earlier successful Runs remain stored.

Memory details expose the latest attempt and active Run separately: a failed latest attempt can coexist with an earlier successful result.

## Scheduling and Rebuild

Automatic Understanding is admitted after import commits. Imports and Rebuilds share a process-local FIFO queue and `understanding.max_concurrent`, defaulting to one worker. Each Memory can have one queued/running attempt. Admitted work continues independently of the client connection.

Rebuild and status use the server API: `202` accepts work; `409` returns the conflicting attempt's handle. Handles expire on replacement, deletion, or restart. [OpenAPI](../api/openapi.yaml) defines these operations and Memory details.

Saved guidance is a mutable Memory preference; successful Runs record the guidance used. Failure retains the new preference and the previous Run's note. Conflicting requests leave the preference unchanged.

## Known limits

- Scheduling is best effort. Shutdown cancels active processes; restart loses pending work and handles and schedules nothing. Retry or reprocessing with changed configuration requires explicit Rebuild.
- Queue length is unbounded. Source content and process output are buffered; document size and concurrency affect memory use.
- Deleting a Memory removes queued work and handles but does not immediately stop an already-running plugin.
- Plugins are trusted local executables. Process separation provides crash containment, not a general sandbox.
- Validation checks result structure. Model accuracy and resistance to document instructions remain best effort.
- Garbage collection and automatic migration of incompatible Vault schemas are deferred.

## Diagnostics and starting points

Normal server logs show lifecycle and progress. Failures retain process output. Optional per-plugin `log_dir` captures stdout/stderr for every execution; paths are relative to the server's working directory. Logs and captures may contain private source content, guidance, and model output.

Use `mem-understand` for an isolated execution through the production runner and validator, capturing the request, output, diagnostics, and artifacts. Start with [diagnostic usage](understanding-plugin-v2.md#local-introspection-command), the [configuration example](../server/memoryd.example.yaml), and the [PDF](../plugins/pdf-text/README.md) and [Codex](../plugins/codex-extractor/README.md) plugin documentation.
