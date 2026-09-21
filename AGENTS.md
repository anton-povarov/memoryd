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

Use subagents for code exploration. Parallelize independent exploration tasks when useful.
Use an explorer when exploration would require reading multiple files, tracing behavior, or investigating an independent question.
For a simple known-file lookup, inspect directly.
- Use GPT 5.6 Luna for explorers. Reasoning medium by default, adjust to low/high based on task difficulty.
- Give each explorer a clear goal and very specific instructions.
- Do NOT pass existing/orchestrator context. Provide only task instructions and context required to perform the task.
- Explorers should complete their assigned task and return concise findings, relevant locations, evidence, and uncertainties.
- Explorer subagents must NEVER MODIFY CODE.
- The main orchestrator owns synthesis and decisions.

### Issue tracker

Local Markdown under `.scratch/`. See `docs/agents/issue-tracker.md`.

### Domain docs

Single-context. See `docs/agents/domain.md`.
