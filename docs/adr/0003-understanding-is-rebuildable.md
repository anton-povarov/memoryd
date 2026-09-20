# Understanding is rebuildable

Status: deferred beyond the MVP. Rebuild is not part of the active API.

A Memory references one immutable content-addressed Blob, while its Derived Content and Facts can be replaced by a Rebuild using a newer understanding implementation. A Rebuild prepares a complete Understanding Run before atomically making it active; failed attempts create only an informative execution log and leave the previous active Run untouched. This allows understanding to improve without duplicating or mutating the thing the user originally entrusted to the Vault.
