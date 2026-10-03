# MemoryD

Local, single-user Personal Memory Vault.

Stores content and medatata, understands it with tools and LLMs and provides natural language search.

Status: MVP in-progress, see [`docs/MVP.md`](docs/MVP.md).

The current implementation imports files up to 100 MiB and supports browsing,
document understanding, ranked keyword search, document preview and download 
through a web UI, CLI, and OpenAPI HTTP interface.

See the [Document Understanding overview](docs/understanding.md) for responsibilities, lifecycle, configuration, and limits.

## Setup

Requires Go 1.25 or newer.

```sh
go mod download
go run ./server/cmd/memoryd
```

Or with a config file
```sh
go run ./server/cmd/memoryd -c server/memoryd.example.yaml
```

## Usage

Web UI: <http://127.0.0.1:8080/>.

CLI command examples:
```sh
go run ./cli/cmd/mem put /absolute/path/to/file
go run ./cli/cmd/mem list
go run ./cli/cmd/mem info <memory-id>
go run ./cli/cmd/mem get <memory-id>
go run ./cli/cmd/mem delete <memory-id>
go build -o mem-search ./cli/cmd/mem-search
./mem-search -n 50 'passports Anton'
./mem-search -n 150 'passports'
./mem-search --all 'Games'
MEMORYD_URL=http://127.0.0.1:8080 ./mem-search 'игры'
```

Use `mem --server ADDRESS ...` or set `MEMORYD_URL` to connect to another
server. Run a command without its required arguments to see its usage.

### Search

See the [search architecture overview](docs/search.md) for structural decisions,
index lifecycle, and known limits.

The web search input sits in the top header alongside the logo and **Add memory**.
Press **Enter** to submit; typing alone does not search. Results show
the original query, mandatory text terms, total matches, and safely highlighted
excerpts. Each result shows its match tier and BM25 score instead of repeating
the Media Type shown by its icon. The score tooltip explains sorting and shows
the full value. Selecting a result opens existing details; the compact back arrow beside
the filename restores loaded results, query, and scroll position. **Load more**
appends another page. Clearing the input restores browsing. Deleting a result
refreshes that query.

`mem-search [--server ADDRESS] [-n N | --all] <quoted phrase>` uses the same server API:
`GET /api/v0/memories/search?query=...&limit=50`. Options precede the phrase.
Server selection follows `--server`, then `MEMORYD_URL`, then the local default.
The positive int64 count defaults to 50 and can span pages; `--all` follows
continuations to exhaustion. An explicitly supplied `-n` cannot accompany `--all`.
JSON stdout contains the original `query_plan`, complete match `total`, and
ordered `items`; empty results succeed. Diagnostics go to stderr with exit
2 for usage errors and exit 1 for operational failures.

Search covers the whole Vault: original filenames, available Import Context
paths, valid UTF-8 `text/*` original Blobs without NUL bytes, saved user notes,
and supported Derived Content from the active Understanding Run. Text artifacts
include descriptions and summaries; JSON artifacts contribute meaningful scalar
values, including current Facts, events, references, and signals, not JSON syntax
or processing metadata. Historical Runs and logs do not match. Opaque original
formats remain searchable through metadata, notes, and supported active artifacts.
Unparseable JSON remains a valid retained artifact but contributes no structured
search values; other supported artifacts and existing fields still participate.
Every compiled word or number is mandatory, but may match a whole word/English
stem or a case-insensitive Unicode substring in any of those fields. `civ2` finds
`oldCIV2backup` in a filename or content; different terms may use different
fields and matching modes. Substrings do not supply aliases: `civ2` does not
automatically mean `Civilization II`.

Complete whole-word/stem matches rank first by word-index BM25. Fragment-dependent
matches follow, ranked by BM25 from the existing trigram index for query terms
of three or more Unicode characters. If no indexed fragments match, ranking
falls back to word-index BM25, or 0 when no whole words match. Pure one/two-character
fragment matches remain tied. All score ties use stable Memory identity.
Each API hit exposes `match_tier` (0 for whole-word/stem matches, 1 for
fragment-dependent matches) and `score` (raw SQLite BM25; lower is better within
each tier). Word and trigram scores use different indexes and are not comparable
across tiers or confidence percentages. Totals include all matching modes even
when precise matches exist, before applying the limit. Filenames and excerpts
highlight literal matching fragments safely. `OR`, connectors, and years remain
literal terms, not operators or date filters. No models run.

Native trigram indexing covers fragments of three or more Unicode characters.
One/two-character fragments also work through a Unicode-aware scan of indexed
text; broad or absent short fragments can become expensive as text volume grows.

The rebuildable projections update with imports, notes, deletions, and successful
Understanding activation in the same database transaction. Failed Rebuilds preserve
the prior active searchable content. Startup backfills missing original rows and
unindexed active Runs using verified stored Blobs, without models or reimport;
missing or corrupt supported content fails startup without erasing original data.
Compatible projection upgrades reuse stored original text, and an already-indexed
active Run is not reread on every startup.

HTTP pages default to 50 and accept limits from 1 to 100. An optional `cursor`
continues the same trimmed query; `next_cursor` is absent at exhaustion.
For an unchanged Vault, pages preserve the combined precise/fragment ordering
without duplicates or omissions. Continuations are not snapshots: concurrent
imports, deletions, notes, or Rebuilds can change ordering between requests.
Search responses use `Cache-Control: no-store`. Fact/date filters and temporal
interpretation remain unimplemented.


- API: <http://127.0.0.1:8080/api/v0>
- API documentation: <http://127.0.0.1:8080/docs/>
- OpenAPI specification: [`api/openapi.yaml`](api/openapi.yaml)

## Development

```sh
./dev-generate-openapi.sh   # regenerate OpenAPI specification
./dev-lint-go.sh            # format and lint Go code
go test ./...               # run all tests
```

`dev-lint-go.sh` requires `golangci-lint` on `PATH`.

Enable the pre-commit hook for this clone:

```sh
git config core.hooksPath .githooks
```
