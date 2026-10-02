This is a Personal Memory Vault project. Store content and medatata, understand it with tools and LLMs and provide natural language search.

Stage: MVP design, see `docs/MVP.md`.
Ubiquitous Language: `CONTEXT.md` 

## Structure
/api                 OpenAPI specification
/cli                 cli programs
/plugins             understanding plugins
/server              server
	/internal/webui    web UI

## Go

When you're done editing Go code, run `dev-lint-go.sh` to format and lint it.
These commands would already perform `go fmt`, `go vet` so no need to run them separately.
Fix issues. If you need to ignore a warning by adding a `nolint` comment, explicitly report it to the user.

To regenerate code from the OpenAPI specification, run `dev-generate-openapi.sh`.

## Project rules
This is an MVP stage project.
Breaking API contracts, database schemas, and file formats is ok, this is an MVP still.
Learning quickly is more important than all corner cases covered.
Make assumptions to simplify solutions, be explicit about them with the user.
You can assume desktop environments, desktop browsers, no need for other platforms yet.

Testing is important, but should be best effort, no need to very deep, things will change quickly.
Smoke tests are good, add extra tests only where critical, do not overengineer.
Testing core assumptions and mechanics that are unlikely to change is useful.

Code quality and simplicity are still important, do not sacrifice them.
APIs, interfaces, cross-process seams are most important, make sure to design them well.


## Agents

### Issue tracker

Local Markdown under `.scratch/`. See `docs/agents/issue-tracker.md`.

### Domain docs

Single-context. See `docs/agents/domain.md`.
