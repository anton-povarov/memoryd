# Codex description context costs

**Measurement date:** 2026-09-30  
**Implementation:** codex-description plugin 0.8.0  
**Runtime:** Codex CLI 0.159.2, `gpt-6-luna`, medium reasoning effort

This note describes the measured v0.8.0 behavior. Version 0.8.1 removes fresh-thread mode and explicit source/transcription resubmission: all stages share one thread, and later synthesis refers to evidence already in that thread. The captures below remain historical evidence; a [DEWA follow-up measurement](../../memoryd-description-capture/prompt-v4/capture-010/comparison.md) now records the fix separately. Plugin source links point to the current implementation, whose prompt helpers differ from v0.8.0.

## Finding 1: measured context and content resubmission

The DEWA PDF capture reports 54,463 total tokens across three stages: 52,172 input tokens and 2,291 output tokens. The input total includes 27,648 cached tokens, leaving 24,524 uncached input tokens. The 52,172 input count is the sum of per-turn context sizes, including repeated conversation prefixes; it is not 52,172 unique document or prompt tokens.

| Stage | Plugin prompt bytes | Output schema bytes | Input tokens | Cached input | Output tokens |
|---|---:|---:|---:|---:|---:|
| Initial description/data | 12,775 | 5,028 | 12,058 | 0 | 231 |
| Visual transcription of requested PDF pages | 903 | 5,028 | 17,737 | 11,008 | 1,599 |
| Final description/data after visual review | 16,401 | 5,028 | 22,377 | 16,640 | 461 |
| **Total** | — | — | **52,172** | **27,648** | **2,291** |

The 5,028-byte schema is sent on every stage. The short 903-byte transcription prompt still has 17,737 input tokens because the second stage runs in the shared thread after the first stage and attaches two page images. Likewise, the third stage follows both earlier turns. Cached input indicates prefix reuse, but each usage entry still reports its full context size. See the [capture report](../../memoryd-description-capture/prompt-v4/capture-002/report.md), [metadata](../../memoryd-description-capture/prompt-v4/capture-002/metadata.json), and [stage/usage log](../../memoryd-description-capture/prompt-v4/capture-002/stderr).

The local plugin explicitly resubmits document-derived content. For PDFs, `source_prompt_content` reconstructs a prompt containing text from all pages; after a visual-review request, the final synthesis rebuilds that content with the visual transcription included. Thus the stage-three prompt repeats the extracted source text and adds the prior visual transcription as text. The visual stage itself sends the rendered pages as `localImage` inputs; the model's transcription response is then included in the final synthesis prompt. This is explicit application-level resubmission in addition to the shared thread's prior-turn context. See [`source_prompt_content`](../../plugins/codex-description/plugin.py), [initial PDF analysis and stage flow](../../plugins/codex-description/plugin.py), [review and final synthesis](../../plugins/codex-description/plugin.py), and [`run_turn` image/schema input](../../plugins/codex-description/plugin.py).

The fresh-thread image comparison, capture 008, provides a useful but not controlled baseline: it reports a 2,892-byte synthesis prompt, 5,028-byte schema, and 10,896 input tokens for synthesis. That synthesis stage has no attached image and no prior-turn history because fresh mode starts a separate thread for each model stage. Its first visual-transcription turn used 11,843 input tokens and did attach the image. The final output preserves a full textual transcription of the source image. The 10,896-token no-image synthesis request shows substantial context beyond its short plugin prompt, but it does not isolate Codex's fixed overhead. See [capture 008 report](../../memoryd-description-capture/prompt-v4/capture-008/report.md), [metadata](../../memoryd-description-capture/prompt-v4/capture-008/metadata.json), and [stage log](../../memoryd-description-capture/prompt-v4/capture-008/stderr).

Images and automatically transcribed scan pages have a related duplication: the transcription is already an assistant response in the shared thread, then the plugin explicitly includes it in the synthesis prompt. Fresh mode needs that explicit transcription because its synthesis thread has no earlier turn.

The plugin saves extracted/transcribed text in the final artifact. Repeating that text in the later synthesis prompt is current behavior; these observations do not establish that prompt repetition is required to preserve output fidelity.

## Finding 2: residual Codex context and limits of attribution

The app-server invocation disables `shell_tool`, `unified_exec`, `view_image`, `skill_search`, legacy `multi_agent`, plugins, apps, browser/computer use, and image generation; it also sets `tools.web_search=false`. It does not set `skip_host_skill_discovery` or disable `multi_agent_v2`. Consequently, disabling the `skill_search` tool alone is not evidence that host-side skill discovery/warming is skipped. The installed CLI's local `codex debug models` output identifies `gpt-6-luna` as using Responses Lite, reports `multi_agent_version: v2`, and reports an 18,043-byte model `instructions_template`. The template byte count is not a token count and cannot be assigned directly to the 12,058-token first request.

Codex's current source builds model instructions/world state per step and has model-specific instruction templates. In its Responses Lite path, the supplied base-instruction content is assembled alongside the host's model/world-state content rather than replacing all of it. See the primary-source implementations in [world_state.rs](https://github.com/openai/codex/blob/main/codex-rs/core/src/session/world_state.rs) and [client.rs](https://github.com/openai/codex/blob/main/codex-rs/core/src/client.rs). These sources support built-in Codex/model context as a likely contributor beyond the plugin's 159-character `baseInstructions`; they do not provide a token-by-token attribution for this installed 0.159.2 run. The source links point to the current upstream branch, not a version-pinned 0.159.2 checkout.

A no-inference probe used the plugin-style `initialize` and `thread/start` parameters with the same disabled-feature flags, model, ephemeral temp CWD, and base instructions. The `thread/start` metadata returned `instructionSources: []`, `multiAgentMode: "explicitRequestOnly"`, and `ephemeral: true`. The empty instruction-source list supports that no AGENTS/instruction files were loaded through that mechanism. `multiAgentMode` is reported here only as returned metadata; it does not establish whether model-level v2 instructions or other collaboration framing contribute context. The probe does not expose effective prompt text, tool schemas, skill catalog text, or model tokenization. Local checks also found an empty `~/.codex/AGENTS.md` and no `~/.codex/agents` directory. Repository `.codex/agents` files were not reported as instruction sources for the temporary thread; this does not prove whether any unrelated process-start configuration affected the installed app-server.

`codex debug prompt-input` was also run without a model turn. It emitted the current CLI session's prompt, not the app-server thread's prompt, so its harness/skill-menu content must not be used to attribute capture 002. The no-inference app-server probe and debug command establish no exact decomposition of the first-turn overhead. Catalog bytes, plugin prompt bytes, schema bytes, and input tokens are different measurements and should not be equated.

The catalog and no-inference probe results above are diagnostic observations from this investigation; their raw output was not retained in the capture directories. The linked document captures contain stage logs and usage, not a serialized effective model request with all system instructions and tool definitions.

The v4 captures show local PDF extraction with `pdftotext` and PDF-page rendering, followed by app-server `localImage` inputs for visual transcription. The plugin disables `view_image` and `skill_search`; the captured stages contain no model tool-call entries or `view_image` call. Thus these runs show no full PDF skill-body read through a model tool call. The matching `thread/start` probe returned no instruction sources, but this does not establish whether a skill description was otherwise present in model context. Image inputs use the same `localImage` pathway. Source-derived text and transcription are preserved in the final artifact; repeating the content in a later synthesis prompt is current behavior, not an identified fidelity requirement. See [`extract_pdf_pages` and rendering](../../plugins/codex-description/plugin.py), [image transcription path](../../plugins/codex-description/plugin.py), and the [capture 002 report](../../memoryd-description-capture/prompt-v4/capture-002/report.md).

## Implications for later optimization work

The measurements separate several possible costs without identifying their exact token shares: plugin prompt and schema bytes, attached image inputs, repeated source/transcription text, conversation history in a shared thread, and built-in Codex/model context. The subsequent fix makes synthesis refer to the prior canonical source/transcription already in the shared thread, instead of rebuilding and resending extracted content. Fresh mode is removed. Prompt-shortening changes must preserve artifact quality and source fidelity. Shared-thread runs show substantial cached input here, while a fresh thread loses same-thread cache reuse; exact savings from either strategy were not measured. These captures are observational, not a controlled estimate of fixed Codex overhead.

## Sources

- Local plugin stage prompts, app-server flags, input construction, PDF extraction, visual transcription, and thread handling: [`plugins/codex-description/plugin.py`](../../plugins/codex-description/plugin.py).
- DEWA v4 capture 002: [report](../../memoryd-description-capture/prompt-v4/capture-002/report.md), [metadata](../../memoryd-description-capture/prompt-v4/capture-002/metadata.json), [stderr](../../memoryd-description-capture/prompt-v4/capture-002/stderr).
- Fresh-thread v4 capture 008: [report](../../memoryd-description-capture/prompt-v4/capture-008/report.md), [metadata](../../memoryd-description-capture/prompt-v4/capture-008/metadata.json), [stderr](../../memoryd-description-capture/prompt-v4/capture-008/stderr).
- Codex model instruction/world-state assembly: [OpenAI Codex `world_state.rs`](https://github.com/openai/codex/blob/main/codex-rs/core/src/session/world_state.rs).
- Codex Responses Lite request assembly: [OpenAI Codex `client.rs`](https://github.com/openai/codex/blob/main/codex-rs/core/src/client.rs).
- Codex host skill discovery/warming path: [OpenAI Codex `session.rs`](https://github.com/openai/codex/blob/main/codex-rs/core/src/session/session.rs).
- Codex project instruction discovery: [OpenAI Codex `agents_md.rs`](https://github.com/openai/codex/blob/main/codex-rs/core/src/agents_md.rs).
- Official model reference: [GPT-6 Luna](https://developers.openai.com/api/docs/models/gpt-6-luna).
