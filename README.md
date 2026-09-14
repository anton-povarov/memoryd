# memoryd

Personal Memory Vault. Current server is a v0 OpenAPI-first skeleton with deterministic stub responses and no persistence.

Top-level areas stay independent. The Go server lives under `server/`; future Understanding Plugins and web UI can use sibling directories.

## Run

```sh
cd server
go run ./cmd/memoryd
```

Use an explicit configuration file when needed:

```sh
cd server
go run ./cmd/memoryd -c memoryd.example.yaml
```

The server binds to `http://127.0.0.1:8080/` by default.

For compact development logs, set `MEMORYD_DEV` to a true Boolean value while writing to a terminal:

```sh
MEMORYD_DEV=1 go run ./cmd/memoryd
```

When both conditions hold, development formatting overrides the configured text or JSON format. Redirected output retains the configured standard `slog` format.

- Interactive API documentation: <http://127.0.0.1:8080/docs/>
- OpenAPI YAML: <http://127.0.0.1:8080/openapi.yaml>
- OpenAPI JSON: <http://127.0.0.1:8080/openapi.json>
- API base: <http://127.0.0.1:8080/api/v0>

## Develop

From `server/`, regenerate transport code after changing `api/openapi.yaml`:

```sh
go generate ./...
```

Verify the server:

```sh
go test ./...
go vet ./...
```
