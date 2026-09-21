This is a Personal Memory Vault project. Store content and medatata, understand it with tools and LLMs and provide natural language search.

Stage: MVP design, see `docs/MVP.md`.

## Structure
/api: OpenAPI specification
/cli: cli programs
/server: server
	/internal/webui: web UI

## Go

Coding standard: see `GO_CODING_STANDARD.md`.

When you're done editing Go code, run `dev-lint-go.sh` to format and lint it.
These commands would already perform `go fmt`, `go vet` so no need to run them separately.
Fix issues. If you need to ignore a warning with `nolint` comment, explicitly report it to the user.

To regenerate code from the OpenAPI specification, run `dev-generate-openapi.sh`.

## Agents

### Code Exploration

Use the `code-explorer` subagent when exploration requires reading multiple
files, tracing behavior across components, or investigating an independent
technical question.

For simple known-file lookups, inspect directly.

Parallelize independent exploration tasks when useful. Prefer narrowly scoped
explorers with distinct questions over one broad exploration task.

When spawning a `code-explorer`:
- Use `fork_turns = "none"` by default.
- Give it a specific question and clear scope.
- Provide only the context needed for that investigation. Summarize relevant
  prior decisions explicitly rather than passing the broader conversation.
- Use medium reasoning by default.
- Use low reasoning for straightforward search and code mapping.
- Use high reasoning when the task requires substantial reasoning about
  control flow, lifetimes, concurrency, invariants, or similarly subtle behavior.

The main agent owns synthesis, architectural decisions, and conclusions.


### Implementation

Use the `code-worker` subagent for bounded implementation tasks once the
relevant behavior, constraints, and design decisions are sufficiently understood.

For small changes that are faster and clearer to implement directly, do so.

When spawning a `code-worker`:
- Use `fork_turns = "none"` by default.
- Give it a concrete implementation goal, scope, and relevant constraints.
- Include established design decisions and important context explicitly rather
  than passing the broader conversation.
- Identify files or components likely involved when known, but let the worker
  verify the actual implementation.
- Use high reasoning by default.
- Use low or medium reasoning for mechanical or highly localized changes.
- Use xhigh reasoning when implementation involves subtle lifetimes,
  concurrency, invariants, complex APIs, or non-obvious interactions.

Parallelize implementation only when tasks have clearly separable ownership.
Avoid concurrent workers modifying the same code or tightly coupled areas.

The main agent owns architecture, decomposition, integration, and final
verification.

### Issue tracker

Local Markdown under `.scratch/`. See `docs/agents/issue-tracker.md`.

### Domain docs

Single-context. See `docs/agents/domain.md`.
