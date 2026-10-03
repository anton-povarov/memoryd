# Deferred temporal search: structured dates and relative queries

Type: Feature
Status: ready-for-agent
Labels: ready-for-agent
Scheduling: Deferred by user; saved for future implementation, not authorized to start now.

## Problem Statement

Whole-Vault keyword and substring search already retrieves Memories through Import Context, supported original text, saved notes, and active Derived Content. Results are ranked, paginated, highlighted, and available through the web UI and mem-search. These capabilities are not remaining work.

Calendar wording still behaves as literal mandatory text. A query such as “Tasleem bills from 2026” cannot require a structured content date in 2026, and “last month” has no calendar interpretation. Matching a year in a filename or summary is not equivalent to matching a date asserted in the active Understanding Run. Users also cannot see which date caused a temporal match or retain a resolved relative interval while paging across a calendar boundary.

The user is postponing this remaining work. This self-contained specification preserves the agreed behavior and verification requirements without starting implementation or restoring the removed latency requirement. Historical specifications and tickets provide provenance, but are not required to understand or implement this scope.

## Solution

When this work is resumed, extend the existing server-side Search Planning and search interface with supported calendar expressions. First normalize dates and periods from active structured JSON and support absolute years, named months, dates, and explicit ranges. Then support demonstrated relative phrases, including “last year” and “last month,” resolved once using submission time and the server's local timezone.

Show the original temporal wording, resolved calendar bounds, and remaining mandatory text terms in the Query Plan. Return each whole Memory whose text satisfies those terms and whose normalized source date or period overlaps the requested interval. Explain matches with available date labels, original values, and evidence. Keep the same interpretation across web Load more and mem-search continuation requests.

Preserve current word/stem and substring retrieval, word/trigram BM25 ranking, score display, and detail/back/clear interactions. Search Planning and execution remain deterministic and model-free. Unsupported wording remains visible as mandatory text rather than being guessed or silently dropped.

## User Stories

1. As a Vault user, I want to search for Tasleem bills from 2026, so that issuer vocabulary and a structured calendar constraint narrow the same result set.
2. As a Vault user, I want a supported standalone year interpreted as a calendar interval, so that I do not have to write its start and end dates.
3. As a Vault user, I want supported named months and dates interpreted within a complete query, so that calendar constraints work alongside ordinary text.
4. As a Vault user, I want an explicit supported date range, so that I can search across a chosen calendar period.
5. As a Vault user, I want date-only searches, so that I can browse Memories by their content dates without inventing text terms.
6. As a Vault user, I want a supported “last year” query, so that I can retrieve Memories from the previous calendar year.
7. As a Vault user, I want a supported “last month” query, so that I can retrieve Memories from the previous calendar month.
8. As a Vault user, I want relative intervals based on submission time and the server's local timezone, so that calendar interpretation has a documented reference.
9. As a Vault user, I want the original temporal wording visible in the Query Plan, so that I can check what Search Planning recognized.
10. As a Vault user, I want exact resolved calendar bounds visible, so that relative or imprecise wording does not hide the executed constraint.
11. As a Vault user, I want the remaining mandatory text terms visible, so that I can verify that names and other substantive constraints were preserved.
12. As a Vault user, I want a small supported set of conversational connectors handled deterministically, so that wording such as “bills from 2026” does not require the content to contain “from.”
13. As a Vault user, I want unresolved substantive wording to remain mandatory, so that search does not manufacture matches by removing constraints.
14. As a Vault user, I want year-containing identifiers to remain text rather than become accidental date filters, so that search does not change their meaning.
15. As a Vault user, I want unsupported temporal expressions preserved as text, so that limited parser coverage remains honest.
16. As a Vault user, I want unsupported larger expressions protected against partial recognition, so that a recognized fragment cannot create a misleading calendar filter.
17. As a Vault user, I want a clear request to simplify several independent temporal expressions, so that search does not invent temporal AND/OR semantics.
18. As a Vault user, I want date-bearing Facts searched even when the active structured JSON has no events, so that sparse Understanding Runs remain useful.
19. As a Vault user, I want event date cells included, so that relevant event dates can satisfy a calendar constraint.
20. As a Vault user, I want source days, months, years, and periods to retain their precision, so that search does not invent exact dates.
21. As a Vault user, I want numeric source dates interpreted as day/month/year, so that 11/09/2026 means September 11.
22. As a Vault user, I want appropriate ISO and named-month source dates normalized, so that existing structured content does not need one spelling.
23. As a Vault user, I want any overlapping normalized source date or period to satisfy the temporal constraint, so that a represented period can match the calendar interval it intersects.
24. As a Vault user, I want source date meanings treated inclusively, so that an invoice issued in 2025 but due in 2026 can match 2026.
25. As a Vault user, I want unsupported source date values to remain searchable as text, so that normalization does not remove existing retrieval behavior.
26. As a Vault user, I want unanchored relative wording in structured content excluded from moving date interpretation, so that a Memory's date does not change whenever I search.
27. As a Vault user, I want dates found only in Markdown, summaries, original text, filenames, or paths excluded from temporal filtering, so that a lexical year match cannot bypass a structured-date constraint.
28. As a Vault user, I want import time and filesystem timestamps excluded from content-date filtering, so that importing an old Memory does not make its content recent.
29. As a Vault user, I want the matching date, label, original value, and evidence shown where available, so that I can understand why a Memory appeared.
30. As a Vault user, I want each Memory returned once even when several dates match, so that repeated Facts or events do not inflate results or totals.
31. As a Vault user, I want only the active Understanding Run to contribute normalized dates, so that superseded interpretations cannot cause stale matches.
32. As a Vault user, I want existing compatible active Runs backfilled from stored JSON without models or reimport, so that temporal search works over my current Vault.
33. As a Vault user, I want successful Understanding activation to replace searchable dates atomically, so that text and temporal interpretation refer to the same active Run.
34. As a Vault user, I want failed Rebuilds to preserve prior searchable dates, so that an operational failure does not hide existing Memories.
35. As a Vault user, I want deletion to remove temporal matches, so that search cannot return a deleted Memory.
36. As a Vault user, I want combined text/date searches to preserve current word-first and trigram BM25 ranking, so that date filtering does not undo established relevance behavior.
37. As a Vault user, I want date-only results ordered by the newest matching normalized source date with deterministic ties, so that their order does not depend on import time.
38. As a Vault user, I want highlighted excerpts where text matches and date explanations where dates match, so that date-only results need not fabricate a content excerpt.
39. As a Vault user, I want empty results to retain the Query Plan, so that I can revise a valid query after seeing its interpretation.
40. As a Vault user, I want interpretation errors distinguished from valid empty results, so that I know when to simplify a query rather than infer that no Memory matches.
41. As a Vault user, I want Load more to preserve the original absolute or relative interval, so that crossing a calendar boundary cannot silently change subsequent pages.
42. As a Vault user, I want complete duplicate-free paging over an unchanged Vault, so that date matches are neither repeated nor omitted.
43. As a Vault user, I want opening a result and going Back to restore the Query Plan, loaded results, and scroll position, so that date search remains navigable.
44. As a Vault user, I want clearing a temporal query to restore browsing, so that existing browse behavior remains available.
45. As a command-line user, I want mem-search to expose the same temporal Query Plan and date explanations as the web UI, so that both clients describe the same server interpretation.
46. As a command-line user, I want limited searches and --all to retain the original calendar constraint across pages, so that complete retrieval does not reinterpret relative wording.
47. As a command-line user, I want valid empty results to succeed and interpretation or operational failures to remain distinguishable, so that scripts can handle each outcome correctly.
48. As a Vault user, I want Search Planning and execution to make no model calls, so that calendar search has no model dependency or search-time model cost.
49. As a developer, I want supported expressions demonstrated through complete queries and real storage, so that isolated parser success does not imply unsupported end-to-end behavior.
50. As a maintainer, I want the remaining work explicitly saved as deferred, so that a ready-for-agent label does not imply permission to implement it now.

## Implementation Decisions

- **Remaining slices:** retain ticket 04 for structured-date normalization and absolute temporal queries, followed by ticket 05 for relative query interpretation and final representative acceptance. Tickets 01, 01b, 02, and 03 are complete prerequisites, not work to repeat. Ticket 05 remains dependent on ticket 04.
- **Scheduling:** this is an implementation-ready design saved for later. The ready-for-agent label describes triage completeness, not current scheduling. Do not implement either slice until the user resumes the work.
- **Existing deep module and interface:** extend the server's existing search operation, Search Planning, SQLite execution, and generated HTTP contract. The web UI and mem-search remain adapters using that interface. Do not add a second search implementation, public planning operation, provider interface, or new process.
- **Parser choice:** integrate the already-installed go-anytime library inside the Go server. It is not yet integrated into current Search Planning. Use supported embedded-expression facilities and demonstrate complete-phrase extraction. Prefer correctness over broad recognition; no Duckling process or parity grammar.
- **Absolute Search Planning:** accept one fully supported temporal expression or one explicit range. Recognized standalone years become visible temporal constraints; longer year-containing identifiers remain text. Preserve unresolved substantive terms and handle only a small deterministic set of connectors. Multiple independent recognized expressions request simplification.
- **Partial recognition:** never silently consume a recognizable fragment of a larger unsupported temporal expression. Unsupported wording remains mandatory text in the Query Plan. Treat user input as data, not raw SQL or FTS syntax.
- **Relative Search Planning:** demonstrate at least last year and last month. Use search submission time and the server's local timezone; resolve the reference and bounds once. Relative wording in a query does not expand source-date extraction or resolve unanchored source wording against search time.
- **Source-date normalization:** read only supported structured JSON in the active Understanding Run, using the existing artifact convention. Include date-bearing Fact values, event date cells, represented months and years, and periods. Do not require an events array to contain dates. Retain available source labels, original values, and evidence.
- **Calendar semantics:** support appropriate ISO, named-month, and numeric source forms, with day/month/year for numeric dates. Preserve day, month, year, and period precision. Unsupported values remain text-searchable; do not guess ambiguous unsupported values or manufacture missing precision.
- **Filtering:** a Memory matches when its remaining mandatory text terms match and any normalized source date or period overlaps the query interval. A recognized year cannot be satisfied solely by a year in prose or a filename. Date meanings are initially interchangeable, including issue and due dates. Deduplicate equivalent dates as appropriate and always return each whole Memory once.
- **Projection and lifecycle:** add a rebuildable normalized-date projection within the existing SQLite database. Stored successful Runs remain authoritative. Backfill compatible existing active Runs from stored JSON without models, reimport, or historical Run mutation. Update dates alongside successful active-Run changes and deletion; a failed Rebuild preserves the previous active searchable content and dates. Keep the current transactional projection design and explicit rejection of incompatible authoritative schemas; do not add general migrations.
- **Ranking:** preserve current word/stem-first and fragment-dependent tiers. Tier 0 uses word-index BM25. Tier 1 uses existing trigram-index BM25 for terms of at least three Unicode characters, falling back to word BM25 or 0 when no indexed fragments match. Pure one/two-character fragment matches remain tied. Lower scores sort first within a tier; ties use Memory identity. Do not restore the legacy word-only fragment-ranking rule or remove score display. Scores from different indexes are not comparable across tiers or confidence percentages.
- **Date-only ordering:** order by the newest matching normalized source date, then deterministic identity. Do not use import or filesystem timestamps or invent a text excerpt. Preserve source precision in the explanation.
- **HTTP contract:** retain the existing query, page limit, total count, result entries, and continuation behavior. Extend Query Plan and result information with the original temporal expression, resolved bounds, the relative reference where applicable, and available date-match explanations. Regenerate the existing server and client bindings. Neither client parses or normalizes dates independently.
- **Continuation:** preserve the original resolved temporal interpretation and reference rather than recompiling against a later clock value. For an unchanged Vault, continuations cover the combined filtered set without duplicates or omissions. This does not create a snapshot across concurrent imports, deletions, note changes, or Rebuilds.
- **Web interactions:** keep explicit Enter submission, the main-view results, initial page of 50, Load more, safe highlighting, existing Memory details, Back restoration, and clearing to browse. Add temporal wording, bounds, and date evidence to the existing visible surfaces. Retain Media Type in the left icon and existing score metadata rather than repeating Media Type as text. Loading, valid empty results, and interpretation errors remain distinct.
- **mem-search:** preserve the existing server selection, positive limited count, --all, option conflicts, JSON stdout, stderr diagnostics, and exit conventions. Return the same Query Plan and date-match information as HTTP, and retain the first interpretation through all continuation requests. Do not add offline storage access or unrelated output modes.
- **Delivery documentation:** each future slice updates supported-query examples, generated contracts, search usage, and relevant product/domain descriptions when its behavior is actually implemented. Document demonstrated supported forms, not presumed parser coverage. Preserve unrelated ADRs and the historical parent specification.

## Testing Decisions

- **Confirmed primary seam:** the user confirmed the existing HTTP search interface backed by real SQLite, CAS storage, and deterministic Understanding data. Reuse this highest existing seam for end-to-end search behavior; do not introduce a new public test interface or harness.
- **Good tests:** assert consumer-visible Memory identities, Query Plan semantics, source precision, date explanations, required terms, ordering, totals, continuations, errors, and lifecycle transitions. Do not pin exact BM25 numbers, incidental wording, generated source, SQL construction, physical tables, third-party parser internals, or mock forwarding.
- **Focused Go tests:** supplement HTTP coverage with deterministic tests of critical Search Planning and source-date normalization logic. Use fixed reference times and timezones through simple internal inputs where needed, not a public clock framework. Cover calendar/year/month boundaries, leap-year cases, precision, period overlap, invalid/unsupported/unanchored values, identifiers, connectors, multiple expressions, and partial-recognition hazards.
- **HTTP/SQLite acceptance for ticket 04:** prove a complete Tasleem/year query; a date-bearing Fact with no events; month/period overlap; the 2025 issue/2026 due case; deterministic date-only order; and exclusion of dates present solely in Markdown, summaries, original text, filename, paths, import time, or filesystem metadata. Preserve text tiers and trigram ranking after filtering and avoid duplicate Memories from repeated date evidence.
- **Lifecycle acceptance:** prove backfill of compatible stored active Runs without Document Understanding, date replacement on successful Rebuild, preservation after failed Rebuild, exclusion of historical Runs and logs, and removal after deletion. Use deterministic artifacts and the existing controllable Understanding Plugin test pattern; assertions concern search outcomes, not the fixture exchange.
- **HTTP/SQLite acceptance for ticket 05:** compare supported relative queries with equivalent explicit calendar intervals at fixed references. Check identities, interpretation, totals, date-only order, and complete paging. Prove a continuation retains the original reference and bounds even when the later request crosses a month or year boundary.
- **CLI smoke:** run the actual mem-search executable against a running server for combined text/date and date-only queries, limits, --all across pages, valid empty results, and unsupported/error cases. Compare identities and Query Plan semantics with direct HTTP behavior. No models run during search.
- **Browser smoke:** submit supported absolute and relative queries in the actual UI; inspect temporal wording, bounds, highlighted text, scores, and date explanations. Exercise Load more, opening a later result, Back restoration, clearing, valid empty results, and interpretation errors. HTTP tests alone are not visual proof.
- **Prior art:** reuse current real-server Handler tests with httptest and temporary storage, search pagination integration tests, active Derived Content/Rebuild tests, Vault search tests, and existing CLI command patterns. Keep one primary integration seam rather than duplicating the search engine behind mock clients.
- **Representative demonstrations:** exercise passports with optional explicit names, Tasleem bills from 2026, Games, Bumble payslips, and Crimson articles about real estate ZPIF where appropriate Memories and vocabulary exist. Report lexical misses honestly; do not add aliases or manufacture vocabulary to claim success. Do not copy private identities or financial data into permanent fixtures.
- **Future verification only:** each resumed implementation slice runs the required Go formatting/lint command and relevant tests, proves its live HTTP/CLI/UI behavior, updates documentation, and removes temporary smoke scaffolding. This specification task does not implement or run the postponed feature.
- **No latency acceptance:** ticket 05's latency measurement and performance-target requirement was removed by the user. Do not inherit the parent specification's under-one-second benchmark, scale experiment, or permanent wall-clock assertions into this remaining work.

## Out of Scope

- Implementing tickets 04 or 05 now, or reimplementing completed whole-Vault search, pagination/navigation, active Derived Content retrieval, trigram ranking, or score display.
- Latency benchmarking or a submission-to-render performance target for this deferred feature.
- Model calls during Search Planning or search execution, embeddings, vector retrieval, and semantic reranking.
- Synonyms, relationship aliases, category inference, Russian morphology, translation, transliteration, cross-language retrieval, or automatic relaxation of mandatory terms.
- Multiple independent temporal expressions, boolean temporal planning, broad stopword deletion, or Duckling-parity grammar.
- Kind-specific date preference, typed person/amount/other Fact filters, filesystem creation-date search, or preferred invoice/billing date semantics.
- Date extraction from Markdown, summaries, original Blobs, filenames, paths, saved free-text notes, import time, or filesystem timestamps; resolving unanchored relative source values against search time.
- Understanding Plugin contract changes, new extraction capabilities, separate indexing workers or queues, extra processes/databases, custom ranking engines, speculative caches, or a general schema-migration framework.
- Per-artifact or chunk-level results, a second Memory detail interface, offline mem-search, or client-side temporal parsing.
- Snapshot isolation of a complete paginated search across concurrent Vault mutations.
- Multi-user, authentication, remote deployment, mobile-specific UI work, or production-scale search infrastructure.

## Further Notes

### Provenance and work order

- Remaining scope comes from [ticket 04](../semantic-search/issues/04-filter-by-structured-calendar-dates.md), [ticket 05](../semantic-search/issues/05-support-relative-temporal-queries.md), and the relevant temporal decisions in the [original semantic search specification](../semantic-search/spec.md). The [original ticket map](../semantic-search/map.md) retains completed-work history and links here for deferred work.
- Resume ticket 04 first; completed tickets 02 and 03 satisfy its prerequisites. Resume ticket 05 only after ticket 04 is complete. The user has not requested a start date or implementation in this task.
- This specification incorporates the later JSON-only inclusive date decision, numeric day/month/year assumption, current trigram BM25 and score display, and removal of ticket 05 latency acceptance. These take precedence over older conflicting statements in the parent specification or ticket text. The parent remains a historical source, not a description of today's implementation.
- FTS5 remains lexical retrieval despite the older feature name “semantic search.” Ranking cannot recover a Memory without matching vocabulary. The Civilization example may miss Games when no searchable content supplies that term; temporal work does not add an alias.

### Postponement impact: ADRs

- [Go, SQLite, and filesystem CAS](../../docs/adr/0007-go-sqlite-and-filesystem-cas.md): no change from postponement. Future normalized dates are rebuildable SQLite projections, not an authoritative schema migration or new database.
- [Understanding is rebuildable](../../docs/adr/0003-understanding-is-rebuildable.md): no change. Future date indexing must preserve atomic active-Run replacement and prior active data after failure.
- [Understanding Plugins are external processes](../../docs/adr/0004-understanding-plugins-are-external-processes.md): no change to the plugin interface from this work or its postponement.
- [Models are routed by task category](../../docs/adr/0005-models-are-routed-by-task-category.md): no change. It already states that Search Planning routing is not implemented; deterministic calendar interpretation does not require a model provider or fallback.
- No inspected ADR commits to delivering temporal search now. No ADR needs to be reversed or amended solely because of this postponement.

### Postponement impact: README

- [README Search](../../README.md#search) already says query years/connectors remain literal text and Fact/date filters and temporal interpretation are unimplemented. Those statements remain correct while the work is deferred; no capability should be advertised as shipped.
- Update that usage section with supported absolute/relative forms, date explanations, and continuation semantics only when the corresponding slice is implemented. Existing keyword search, trigram BM25, scores, and pagination remain available and are not postponed.

### Postponement impact: MVP scope

- [MVP snapshot](../../docs/MVP.md) already identifies broader Fact/temporal search as design goals, describes current literal keyword Search Planning, and assigns calendar interpretation and structured-date filtering to later slices. Postponement does not remove implemented functionality or require a scope reversal.
- Demonstration scenarios mentioning Tasleem bills from 2026 remain future temporal goals, not current acceptance claims. The scenario “Show PDFs created during a specified period” is not delivered by this JSON-only content-date plan: filesystem/import creation timestamps are explicitly excluded. It needs separate scope if it remains a desired demonstration.
- General typed Fact filtering remains deferred beyond these two tickets. Completing temporal search must not claim arbitrary Fact predicates or model-routed Search Planning.
- A pre-existing documentation discrepancy is independent of postponement: the MVP Search section still describes word-only fragment BM25 and fragment-only identity ties, while current search uses trigram BM25 for indexed fragments. Do not restore that old ranking behavior when implementing dates. The current README and this specification describe the accepted ranking.

### Historical specification differences

The parent specification still contains obsolete baseline descriptions, word-only fragment ranking, numeric scores as out of scope, repeated Media Type text, a latency goal, and older notes about MVP assumptions. This new specification intentionally carries forward only the remaining temporal work and current accepted constraints. No parent-spec rewrite, unrelated ADR change, or feature implementation is part of publishing this deferred plan.
