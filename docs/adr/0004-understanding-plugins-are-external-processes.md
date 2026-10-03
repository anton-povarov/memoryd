# Understanding Plugins are external processes

Status: accepted.

Understanding Plugins run as child processes behind a small JSON-over-standard-streams protocol. This lets memoryd use extraction tools written in other languages and contain a plugin crash without crashing the server.

Memoryd launches an explicitly configured executable per attempt, bounds concurrent processes, and owns Run commits. Scheduling is process-local and best effort, as recorded in [Understanding is rebuildable](0003-understanding-is-rebuildable.md).

Plugin discovery, installation, and capability manifests are deferred. The process contract, request/result schema, and failure rules are in [Understanding plugin protocol v2](../understanding-plugin-v2.md).
