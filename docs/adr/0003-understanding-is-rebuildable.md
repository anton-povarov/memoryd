# Understanding is rebuildable

Status: accepted. Manual Rebuild is exposed through the desktop Refresh button and `POST /api/v0/memories/{memoryId}/rebuild`.

A Memory references one immutable content-addressed Blob, while its Derived Content and Facts can be replaced by a Rebuild using a newer understanding implementation. A Rebuild prepares a complete Understanding Run before atomically making it active; failed attempts create only an informative execution log and leave the previous active Run untouched. This allows understanding to improve without duplicating or mutating the thing the user originally entrusted to the Vault.

Refresh accepts best-effort work in a process-local queue using current plugin
configuration. This supersedes the previous durable scheduling and startup retry
guarantees: restart drops queued/running jobs, and startup schedules nothing.
Only successful Runs and terminal failure history are persisted; the original
Blob and previous active Run remain intact on failure.

New work returns 202 with a polling handle at
`GET /api/v0/rebuild-status/{rebuildId}`. Competing queued/running work returns
409 with the existing handle and does not replace its originating request ID.
The latest attempt, including terminal diagnostics, remains in RAM until a newer
attempt is accepted, Memory deletion, or restart. Unknown handles return 404;
clients reload durable state without automatically starting replacement work.
Background logs retain the initiating HTTP request ID without HTTP cancellation.
Automatic polling remains read-only; configuration changes alone do not trigger
a Rebuild. Model-backed Rebuilds may incur cost.
