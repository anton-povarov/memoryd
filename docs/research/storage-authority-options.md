# Storage authority options for memoryd

Status: deferred research note; **not the current architecture and not an accepted ADR**  
Date: 2026-09-14  
Scope: whether Memory metadata should be authoritative in SQLite or stored as immutable content-addressed records

## Executive summary

The MVP architecture remains unchanged: imported content is stored as immutable Blobs in a filesystem content-addressable store (CAS), while SQLite is authoritative for Memories, Facts, Understanding Runs, active-Run selection, processing state, and full-text search. Losing SQLite therefore loses durable metadata even if every source Blob survives. This is the simpler design with which to finish the product interview and validate memoryd's ingestion and retrieval behavior.

A promising future direction is a deliberately small **Perkeep-lite** design. In it, memoryd would store a few versioned immutable metadata records alongside content in CAS and treat SQLite/FTS5 as a disposable projection. This could make the meaningful Vault state reconstructible without adopting Perkeep's generalized permanodes, signed claims, multi-writer semantics, and related complexity.

The group agreed that this direction is worth returning to later, with two desired properties:

1. Deleting SQLite and rebuilding it should be able to reproduce the same Memories, Facts, active Understanding Runs, Derived Content, and logs.
2. Operational pending or running jobs do not need to be reconstructible after SQLite loss. Work can instead be rediscovered or retried from durable Memory state; losing a pending manual Rebuild request is acceptable.

These are research goals, not current MVP guarantees.

## Why Perkeep is relevant

Perkeep's fundamental storage operation is putting and retrieving immutable blobs by content digest. Its overview says that content and metadata are both represented as content-addressed blobs. Its index is a derivative of blob storage: if the index database is lost, it can be deleted and populated again from stored blobs ([Perkeep overview](https://perkeep.org/doc/overview.md)). The documented reindex implementation wipes its index key/value store, enumerates the source blobs, indexes them, checks unresolved dependencies, and rebuilds its deletion cache ([Perkeep index reindex implementation](https://perkeep.org/pkg/index/index.go?s=36419:36542)). Perkeep's server configuration makes the separation concrete by allowing SQLite and other databases to serve as interchangeable index backends ([Perkeep server configuration](https://perkeep.org/doc/server-config)).

Perkeep also supports mutable user-visible objects without mutating stored blobs. A permanode is an immutable, stable anchor; timestamped signed claim blobs add, set, or remove its attributes. The current object state is obtained by combining applicable claims in order ([Perkeep permanodes](https://perkeep.org/doc/schema/permanode.md)). Perkeep's broader schema includes files, directories, static sets, permanodes, claims, and other metadata forms as versioned JSON schema blobs ([Perkeep schema](https://perkeep.org/doc/schema/)).

That model demonstrates the valuable invariant under discussion: content and metadata can be authoritative in immutable storage while a query database remains disposable. It does not mean memoryd should copy the entire Perkeep design.

## Option A: current MVP architecture

```text
Filesystem CAS (authoritative)     SQLite / FTS5 (authoritative)
------------------------------     -----------------------------
immutable imported bytes           Memories and import metadata
                                   Facts and Derived Content
                                   Understanding Runs
                                   active Understanding Run
                                   processing state and logs
                                   browse and full-text indexes
```

SQLite is not merely an index in this design. It is part of the Vault's durable state and must be backed up together with the filesystem CAS.

### Benefits

- It uses ordinary SQL transactions for relationships, constraints, and active-Run changes.
- Schema evolution can use conventional SQLite migrations.
- Facts and FTS documents do not need a separate canonical serialization format.
- Import and understanding code can be built before designing a metadata log, reducer, or reindexer.
- It is the shortest route to testing whether the product's ingestion, parser, query-planning, and browsing model is useful.

### Costs and risks

- Losing SQLite preserves source bytes but loses filenames, provenance, Facts, Runs, Derived Content references, active-Run selection, and logs.
- Backups must capture a mutually consistent SQLite state and all CAS objects referenced by that state. Copying the directories independently while writes continue can produce a mismatched snapshot.
- A logical operation spans two persistence systems. Code must define write ordering and recover from crashes between a CAS write and its SQL commit.
- Retrofitting immutable authoritative metadata later requires a migration that converts existing SQL rows into canonical records and verifies that their projection reproduces current behavior.

### Plausible crash behavior

For import, memoryd can write and atomically rename the content Blob before committing its Memory row. A crash after the Blob write but before the SQL commit leaves an unreferenced Blob, which is tolerable while garbage collection is out of scope. A SQL transaction should make the Memory visible only when all required authoritative metadata is committed.

For understanding, the new Run should be prepared completely and then activated in one SQL transaction. A crash or failed tool invocation must leave the previous successful active Run intact. The present design may track `InProgress`, `Done`, or `Failed` in SQLite and retry interrupted work at startup.

## Option B: full Perkeep-style immutable claims

```text
CAS (authoritative)
  content blobs
  schema/metadata blobs
  stable permanode anchors
  signed, timestamped mutation claims

SQLite or another index
  disposable projection of the blob graph and reduced claims
```

This is the strongest form of the approach. It gives mutable objects stable identities while retaining an immutable history. Perkeep requires signed claims, and reduces claims in order to obtain a permanode's current attributes ([Perkeep permanodes](https://perkeep.org/doc/schema/permanode.md)). Its terminology explicitly distinguishes immutable blobs from mutable objects built as collections of claims ([Perkeep terminology](https://perkeep.org/doc/terms)). The index stores derived keys for blob metadata, claims, and attribute searches rather than acting as the sole copy of that information ([Perkeep index package](https://perkeep.org/pkg/index)).

For memoryd's current scope, however, this would introduce machinery before there is a demonstrated need for it:

- canonical schema-blob serialization and versioning;
- stable object anchors plus claim reduction and ordering rules;
- signing identities and signature verification;
- concurrent or conflicting writers and deterministic conflict handling;
- reachability, roots, deletion claims, and eventual garbage collection;
- projection checkpoints and complete reindexing behavior;
- repair rules for missing references, malformed claims, and forks.

Perkeep uses this general machinery for synchronization, collaboration, and many object types. memoryd's laptop-only MVP has one server writer, no deletion or GC requirement, and no multi-device merge semantics. Adopting the full system now would optimize for deferred problems and enlarge the correctness surface substantially.

## Option C: deferred Perkeep-lite direction

Perkeep-lite would retain the reconstructibility property but define only the record types memoryd presently needs:

```text
Filesystem CAS (authoritative)
  source Blob
  MemoryManifest/v1
  UnderstandingRun/v1
  Derived Content Blob(s)
  AttemptLog/v1 or log Blob
  MemoryRevision/v1 -> prior revision + selected active Run

SQLite / FTS5
  disposable projection for browse, filtering, and full-text search
  operational processing state and ephemeral jobs
```

Possible record responsibilities:

- `MemoryManifest/v1` identifies the immutable source Blob and records intrinsic/import metadata such as original filename, browser-relative or full source path when available, media type, size, and observed timestamps.
- `UnderstandingRun/v1` records extractor/plugin/model versions, its typed Facts, references to Derived Content, and a log reference. Successful Runs are immutable and coexist.
- `AttemptLog/v1`, or a referenced immutable log Blob, preserves selected execution information. The precise treatment of failed attempts remains a design choice; logs should not accidentally become an unbounded duplicate store for sensitive document content.
- `MemoryRevision/v1` references its predecessor and declares which successful Understanding Run is active. This makes active-Run selection durable without changing prior records.

This is intentionally a domain-specific revision model, not a generalized claim language. It would not initially include signatures, arbitrary attribute mutation, user identities, deletion claims, multi-device merging, or garbage collection.

### Crash consistency

A possible import sequence is:

1. Write and atomically publish the source Blob.
2. Write and atomically publish its `MemoryManifest/v1`.
3. Project the manifest into SQLite.

A possible understanding sequence is:

1. Write Derived Content and the execution log.
2. Write a complete `UnderstandingRun/v1` referring to those objects.
3. Write a `MemoryRevision/v1` that selects the Run as active.
4. Project the new revision into SQLite.

A crash can leave unreachable immutable objects or a projection that lags CAS, but it should not expose a partially constructed Run as active. On startup or explicit repair, the projection can replay valid authoritative records. This requires precise rules for atomic file publication, record validation, graph reachability, revision-tip selection, and resuming a partially completed projection.

Because the MVP has one server writer, memoryd could initially require a single linear revision chain per Memory. A future multi-device writer would force explicit fork and merge semantics and should not be implied by this simplified design.

### Schema evolution

Every authoritative record requires a stable, canonical encoding and an explicit version. Readers must continue to understand old versions or migrate by appending newer records; rewriting an old content-addressed object changes its identity. The format must define normalization details such as time encoding, numeric representation, absent versus null values, Fact types/cardinality, and deterministic hashing.

SQLite migrations would still exist, but only for the projection. A complete rebuild should be able to create the newest projection schema directly from all supported durable record versions. This is more work than SQL-only migration, but it decouples preservation from the current database layout.

### Backup and recovery

Under Perkeep-lite, a complete backup primarily means a consistent copy of the authoritative CAS. SQLite can also be copied for fast restoration, but should be disposable. The backup protocol still needs a stable enumeration boundary: blindly copying a live directory while records are being appended can omit objects referenced by a copied manifest or revision. Viable later approaches include pausing commits briefly, taking a filesystem snapshot, or writing a root/catalog record that closes over a known reachable set.

Recovery should verify hashes and references before projection. The essential acceptance test would be:

1. populate a Vault with multiple Memories and rebuilt Understanding Runs;
2. capture the visible Memories, Facts, active Runs, Derived Content, and logs;
3. delete only SQLite;
4. rebuild it exclusively from CAS records;
5. verify semantically identical visible state and search documents.

Pending/running operational jobs are intentionally excluded. After index loss, memoryd may derive missing initial-understanding work from durable Memory state, but it need not reconstruct an exact job queue or a pending manual Rebuild request.

## The Vault module seam

Whichever storage design is chosen, callers should not coordinate filesystem and SQLite writes themselves. A small, deep Vault module can own content hashing, atomic object publication, SQL transactions or projection, active-Run rules, and recovery:

```go
type Vault interface {
    Import(ctx context.Context, input ImportInput) (Memory, error)
    CommitUnderstanding(ctx context.Context, input UnderstandingInput) (UnderstandingRun, error)
    RecordFailedAttempt(ctx context.Context, input AttemptInput) error
    GetMemory(ctx context.Context, id MemoryID) (MemoryDetail, error)
    RebuildProjection(ctx context.Context) error // future Perkeep-lite only
}
```

The exact interface will evolve, but the architectural constraint is useful now: HTTP handlers, extractors, plugins, and query code should see a coherent Vault API, not independent CAS and SQL repositories. That keeps the present implementation replaceable if Perkeep-lite later proves worthwhile.

## Comparison

| Concern | Current SQLite-authoritative design | Full Perkeep-style design | Deferred Perkeep-lite direction |
|---|---|---|---|
| MVP implementation cost | Lowest | Highest | Moderate |
| Source content authority | Filesystem CAS | CAS | CAS |
| Metadata authority | SQLite | Signed schema and claim blobs | Small set of versioned CAS records |
| Is SQLite disposable? | No | Yes | Intended to be |
| Mutable state | SQL updates/transactions | Reduced signed claims over permanodes | Memory revision chain selecting active Run |
| Multi-writer semantics | Not provided | Fundamental part of the model | Explicitly deferred |
| Schema evolution | SQL migrations | Versioned schemas plus claim compatibility | Versioned records plus projection migrations |
| Crash residue | Orphaned Blobs or interrupted SQL state | Unreferenced blobs/claims and lagging index | Unreachable records and lagging projection |
| Backup unit | Coordinated SQLite + CAS snapshot | Authoritative blob storage | Authoritative CAS; SQLite optional |
| Recovery after index loss | Metadata cannot be reconstructed | Reindex all blobs | Intended full metadata re-projection |

## Recommendation and decision status

Continue with the current SQLite-authoritative metadata plus filesystem-CAS architecture through the grilling session and MVP design. Do not silently introduce immutable metadata records into implementation work yet.

Return to Perkeep-lite before the persistence layer becomes expensive to migrate—ideally when defining the concrete SQLite schema, CAS layout, backup contract, or first multi-device roadmap. At that point, prototype canonical record encoding and the delete-SQLite/rebuild acceptance test before accepting an ADR.

**Decision status:** Perkeep-lite is a favored research direction only. It is neither the current memoryd architecture nor an accepted architectural decision.

## Primary sources

- [Perkeep overview](https://perkeep.org/doc/overview.md)
- [Perkeep schema](https://perkeep.org/doc/schema/)
- [Perkeep permanodes and claims](https://perkeep.org/doc/schema/permanode.md)
- [Perkeep terminology](https://perkeep.org/doc/terms)
- [Perkeep server configuration and index backends](https://perkeep.org/doc/server-config)
- [Perkeep index package](https://perkeep.org/pkg/index)
- [Perkeep index reindex implementation](https://perkeep.org/pkg/index/index.go?s=36419:36542)
