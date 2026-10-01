# Understanding Plugins are external processes

Status: active for the MVP's first PDF extractor.

Understanding Plugins run as child processes behind a small JSON-over-standard-streams protocol. This lets memoryd use extraction tools written in other languages and contain a plugin crash without crashing the server.

For the MVP, memoryd starts executables named explicitly in local configuration. The first plugin produces Derived Content from PDFs. It may extract text, perform OCR, or describe visual content, and it need not assert Facts. Memoryd bounds the total number of concurrent understanding processes and owns durable scheduling and Run commits.

Plugin discovery, installation, and capability manifests are deferred. The process contract, request/result schema, and failure rules are in [Understanding plugin protocol v2](../understanding-plugin-v2.md).
