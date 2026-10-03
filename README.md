# MemoryD

Local, single-user Personal Memory Vault.

Stores content and medatata, understands it with tools and LLMs and provides natural language search.

Status: MVP in-progress, see [`docs/MVP.md`](docs/MVP.md).

The current implementation imports files up to 100 MiB and supports browsing,
document understanding, ranked keyword search, document preview and download 
through a web UI, CLI, and OpenAPI HTTP interface.

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
```

mem-search tool:
```sh
go run ./cli/cmd/mem-search -n 50 'passports Anton'
go run ./cli/cmd/mem-search -n 150 'passports'
go run ./cli/cmd/mem-search --all 'Games'
```

Use `--server ADDRESS ...` or set `MEMORYD_URL` to connect to another
server. Run a command without its required arguments to see its usage.

## Documentation

- API docs endpoint: <http://127.0.0.1:8080/docs/>
- API spec endpoint: <http://127.0.0.1:8080/openapi.json>
- OpenAPI specification: [`api/openapi.yaml`](api/openapi.yaml)
- Search architecture overview: [`docs/search.md`](docs/search.md)
- Document Understanding overview: [`docs/understanding.md`](docs/understanding.md)

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
