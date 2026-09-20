# Use Go, SQLite, and a filesystem CAS for the server

The main memoryd server is a Go application. The currently implemented slice
stores immutable Blob bytes in a local filesystem content-addressed store and
uses SQLite as the authoritative store for committed Memories and their Import
Context. A checked-in OpenAPI specification is the HTTP contract and generates
the Go server and CLI bindings through one pinned toolchain.

SQLite separates imported Memory state from understanding state. `memories`
contains only the Blob reference and import metadata. Successful immutable Runs
belong in `understanding_runs`, while `active_understanding_runs` selects at
most one active Run per Memory without making activation a mutable property of
the Run. The current import path creates no Run.

SQLite Schema migrations are intentionally NOT done the Vault can be rebuilt by reimporting content.
Explicit error is emitted on startup if existing database schema is incompatible with the expected one.

Import ends after the Blob is durably and atomically published and its Memory
is committed. A crash between those operations may leave a published Orphan
Blob, which startup preserves because garbage collection is out of scope.
Startup removes only abandoned import and publication staging files. Downloads
verify the committed size and digest before returning Blob content.
