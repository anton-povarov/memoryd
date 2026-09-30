"""Extract document content with two turns on a local Codex app-server thread."""

import base64
import binascii
import hashlib
import json
import os
from pathlib import Path
import queue
import re
import shlex
import subprocess
import sys
import tempfile
import threading
import time


PLUGIN_VERSION = "0.1.0"
TURN_TIMEOUT_SECONDS = 600
PRICING_DATE = "2026-09-30"
PRICING_URL = "https://developers.openai.com/api/docs/models/gpt-6-luna"
# Standard API rates in USD per million tokens, short-context tier.
LUNA_INPUT_RATE = 0.10
LUNA_CACHED_INPUT_RATE = 0.01
LUNA_CACHE_WRITE_RATE = 0.125
LUNA_OUTPUT_RATE = 0.50
DISABLED_CODEX_FEATURES = (
    "skill_search",
    "multi_agent",
    "plugins",
    "apps",
    "browser_use",
    "computer_use",
    "image_generation",
)
ENABLED_CODEX_FEATURES = ("shell_tool", "unified_exec", "view_image")

# MARKDOWN_PROMPT = """
# You are a smart document content extractor.
#
# Do not use skills.
#
# Analyze the attached original document and import context below.
# Do not preserve the source layout or invent details.
#
# Use English for a concise description, the document's original language for a grounded summary,
# and a Markdown table for relevant structured data with concise English keys and original-language values.
#
# Include only source-supported information.
#
# Return Markdown with exactly these headings, in this order:
#
# # Description
# # Summary
# # Structured data
#
# Filename: {filename}
# Import context:
# {import_context}"""

MARKDOWN_PROMPT = """
You are a smart document content extractor.
Analyze the attached original artefact and import context below.

Reply with a concise description and a grounded summary, both in English.

For PNG or JPEG inputs, use Codex's `view_image` tool on the supplied local document path. Do not treat image bytes as text. If an image has no readable text or structured data, describe only visible content and say so; do not invent text, numbers, identifiers, or context.

Do not preserve original document layout.
Do not invent missing content or details.

DO NOT USE SKILLS.

Reply with the following Markdown structure:

<response-markdown>

# Description

A concise description of what the document is and contains.

# Summary

Extract the important textual summary in normalized Markdown.
Preserve semantic information, not visual layout.
Omit decorative, repetitive, or irrelevant UI text unless it contributes meaning.

# Structured data

Extract relevant structured data from the document, formatted as Markdown table.
Whenever the document contains table-like structured data and it's relevant to extract, reproduce it as a separate Markdown table.
Always use English for structured keys.

</response-markdown>

Filename
{filename}

Import context:
{import_context}
"""

JSON_PROMPT = """Return the document's relevant structured data as JSON matching the response schema. The original document remains attached in this thread and is authoritative; use the first-turn Markdown only as a guide, and correct it against the source. Preserve original-language values, use English semantic keys, and include only grounded information. For PNG or JPEG inputs, inspect the supplied local path with Codex's `view_image` tool. If the image has no readable text or structured data, leave facts, events, references, and signals empty and note the absence in uncertainties; do not invent text or data. Return JSON only."""


DOCUMENT_DATA_SCHEMA = {
    "type": "object",
    "description": (
        "Relevant source-grounded information. Preserve original-language values and include "
        "brief source evidence. Use English snake_case fact keys and empty arrays when a category "
        "has no relevant entries. Each category uses positional rows in the documented order."
    ),
    "properties": {
        "facts": {
            "type": "array",
            "description": "Atomic facts stated in the document.",
            "items": {
                "type": "array",
                "description": (
                    "[key, value, evidence]: English snake_case fact name, original-language "
                    "source value, short source wording or location. All positions are strings."
                ),
                "minItems": 3,
                "maxItems": 3,
                "items": {"type": "string"},
            },
        },
        "events": {
            "type": "array",
            "description": "Important actions or changes described by the document.",
            "items": {
                "type": "array",
                "description": (
                    "[action, date, details, evidence]: concise source-language action, source "
                    "date or null, brief source-language detail, short source wording or location. "
                    "Only date (position 1) may be null; all other positions are strings."
                ),
                "minItems": 4,
                "maxItems": 4,
                "items": {"type": ["string", "null"]},
            },
        },
        "references": {
            "type": "array",
            "description": "Identifiers and pointers to other documents or objects.",
            "items": {
                "type": "array",
                "description": (
                    "[kind, value, title, relation, evidence]: English reference type, source "
                    "reference value, source title or null, source-language relation or null, "
                    "short source wording or location. Only title and relation (positions 2 and 3) "
                    "may be null; all other positions are strings."
                ),
                "minItems": 5,
                "maxItems": 5,
                "items": {"type": ["string", "null"]},
            },
        },
        "signals": {
            "type": "array",
            "description": "Observable properties such as document type, title, sender, or issuer.",
            "items": {
                "type": "array",
                "description": (
                    "[kind, value, evidence]: English signal category, observed original-language "
                    "value, short source wording or visual clue. All positions are strings."
                ),
                "minItems": 3,
                "maxItems": 3,
                "items": {"type": "string"},
            },
        },
        "uncertainties": {
            "type": "array",
            "description": "Important ambiguities that affect interpretation, in the source language.",
            "items": {"type": "string"},
        },
    },
    "required": ["facts", "events", "references", "signals", "uncertainties"],
    "additionalProperties": False,
}


class WorkflowLogger:
    """Write readable model conversation and concise workflow stages to stderr."""

    def __init__(self):
        self.started_at = time.monotonic()
        self.lock = threading.Lock()
        self.active_stream = None
        self.stream_ends_with_newline = True
        self.streamed = {}
        self.mcp_arguments_logged = set()

    def _heading(self, text):
        elapsed = time.monotonic() - self.started_at
        print(f"[codex-extractor +{elapsed:.2f}s] {text}", file=sys.stderr, flush=True)

    def _close_stream(self):
        if self.active_stream is not None:
            if not self.stream_ends_with_newline:
                print(file=sys.stderr)
            print("--- End live response ---", file=sys.stderr, flush=True)
            self.active_stream = None

    @staticmethod
    def _label(name):
        label = re.sub(r"([a-z])([A-Z])", r"\1 \2", name)
        return label.replace("_", " ").replace(".", " ").capitalize().replace(" id", " ID")

    @staticmethod
    def _json_block(label, value):
        content = json.dumps(value, ensure_ascii=False, indent=2)
        print(f"--- {label} ---", file=sys.stderr)
        sys.stderr.write(content)
        if not content.endswith("\n"):
            sys.stderr.write("\n")
        print(f"--- End {label.lower()} ---", file=sys.stderr)

    @staticmethod
    def _format_usage(value):
        names = (
            ("input_tokens", "input"),
            ("cached_input_tokens", "cached input"),
            ("cache_write_input_tokens", "cache write input"),
            ("output_tokens", "output"),
            ("reasoning_output_tokens", "reasoning"),
            ("total_tokens", "total"),
        )
        return ", ".join(
            f"{label}={value[key]:,}"
            for key, label in names
            if key in value
        )

    def _field(self, key, value, indent="  "):
        label = {
            "usage": "Token usage",
            "usage_delta": "Token usage",
            "cost_estimate": "API-equivalent cost",
            "cost_unavailable_reason": "Cost unavailable",
            "usage_missing_reason": "Usage unavailable",
            "local_document": "Local document reference",
            "command_output": "Command output",
            "output_schema": "Output schema",
        }.get(key, self._label(key))
        if value is None:
            return
        if key in {"usage", "usage_delta"} and isinstance(value, dict):
            print(f"{indent}{label}: {self._format_usage(value)}", file=sys.stderr)
        elif key == "cost_estimate" and isinstance(value, dict):
            amount = value.get("amount_usd")
            if isinstance(amount, (int, float)) and not isinstance(amount, bool):
                print(f"{indent}{label}: ${amount:.9f} USD", file=sys.stderr)
            else:
                print(f"{indent}{label}: unavailable", file=sys.stderr)
        elif key in {"output_schema", "structured_content"}:
            self._json_block(label, value)
        elif isinstance(value, str) and "\n" in value:
            print(f"--- {label} ---", file=sys.stderr)
            sys.stderr.write(value)
            if not value.endswith("\n"):
                sys.stderr.write("\n")
            print(f"--- End {label.lower()} ---", file=sys.stderr)
        elif isinstance(value, dict):
            print(f"{indent}{label}:", file=sys.stderr)
            for child, content in value.items():
                self._field(child, content, indent + "  ")
        elif isinstance(value, list):
            if key == "command" and all(isinstance(item, str) for item in value):
                print(f"{indent}{label}: {shlex.join(value)}", file=sys.stderr)
            elif all(isinstance(item, (str, int)) for item in value):
                print(f"{indent}{label}: {', '.join(map(str, value)) or 'none'}", file=sys.stderr)
            else:
                print(f"{indent}{label}:", file=sys.stderr)
                for index, content in enumerate(value, start=1):
                    self._field(f"entry {index}", content, indent + "  ")
        else:
            if (key.endswith("reason") or key in {"stage_name", "phase", "status", "item_type"}) and isinstance(value, str):
                value = re.sub(r"([a-z])([A-Z])", r"\1 \2", value).replace("_", " ")
            print(f"{indent}{label}: {value}", file=sys.stderr)

    def _arguments(self, value):
        print("  Arguments:", file=sys.stderr)
        if isinstance(value, dict):
            for key, content in value.items():
                self._field(key, content, "    ")
        elif isinstance(value, list):
            for index, content in enumerate(value, start=1):
                self._field(f"entry {index}", content, "    ")
        else:
            self._field("value", value, "    ")

    def _mcp_result(self, result):
        if not isinstance(result, dict):
            self._json_block("Result", result)
            return

        if "structuredContent" in result:
            self._json_block("Structured content", result["structuredContent"])

        content = result.get("content")
        if isinstance(content, list):
            for index, block in enumerate(content, start=1):
                block_type = block.get("type", "unknown") if isinstance(block, dict) else "unknown"
                if isinstance(block, dict) and block_type == "text" and isinstance(block.get("text"), str):
                    print(f"--- Result text {index} ---", file=sys.stderr)
                    sys.stderr.write(block["text"])
                    if not block["text"].endswith("\n"):
                        sys.stderr.write("\n")
                    print("--- End result text ---", file=sys.stderr)
                elif isinstance(block, dict) and block_type in {"image", "audio"}:
                    mime = block.get("mimeType", block.get("mime_type"))
                    data = block.get("data")
                    encoded = (
                        f"encoded size {len(data)} bytes omitted"
                        if isinstance(data, str)
                        else "encoded data not supplied"
                    )
                    mime_label = f", {mime}" if mime else ""
                    print(f"  Result content: {block_type}{mime_label}; {encoded}", file=sys.stderr)
                elif isinstance(block, dict) and block_type == "resource":
                    resource = block.get("resource")
                    while isinstance(resource, dict) and isinstance(resource.get("resource"), dict):
                        resource = resource["resource"]
                    if isinstance(resource, dict) and isinstance(resource.get("text"), str):
                        print(f"--- Result resource text {index} ---", file=sys.stderr)
                        sys.stderr.write(resource["text"])
                        if not resource["text"].endswith("\n"):
                            sys.stderr.write("\n")
                        print("--- End result resource text ---", file=sys.stderr)
                    else:
                        resource_type = "embedded resource"
                        mime = resource.get("mimeType") if isinstance(resource, dict) else None
                        data = resource.get("blob") if isinstance(resource, dict) else None
                        encoded = (
                            f"encoded size {len(data)} bytes omitted"
                            if isinstance(data, str)
                            else "binary data omitted"
                        )
                        mime_label = f", {mime}" if mime else ""
                        print(f"  Result content: {resource_type}{mime_label}; {encoded}", file=sys.stderr)
                else:
                    safe_block = dict(block) if isinstance(block, dict) else block
                    if isinstance(safe_block, dict):
                        safe_block.pop("data", None)
                        if isinstance(safe_block.get("resource"), dict):
                            safe_resource = dict(safe_block["resource"])
                            safe_resource.pop("blob", None)
                            safe_block["resource"] = safe_resource
                    self._json_block(f"Result content {index}", safe_block)
        elif "content" in result:
            self._json_block("Result content", result["content"])
        elif "structuredContent" not in result:
            self._json_block("Result", result)

    def _mcp_event(self, name, title, metadata):
        self._heading(title)
        for key in ("server", "tool", "status", "message"):
            if key in metadata and metadata[key] is not None:
                self._field(key, metadata[key])

        item_id = metadata.get("item_id")
        arguments = metadata.get("arguments")
        if name == "mcp.call.started" and arguments is not None:
            self._arguments(arguments)
            if item_id is not None:
                self.mcp_arguments_logged.add(item_id)
        elif name in {"mcp.call.completed", "mcp.call.failed"}:
            if item_id not in self.mcp_arguments_logged and arguments is not None:
                self._arguments(arguments)
            if item_id is not None:
                self.mcp_arguments_logged.discard(item_id)

            error = metadata.get("error")
            if error is not None:
                if isinstance(error, (dict, list)):
                    self._json_block("Tool error", error)
                else:
                    self._field("error", error)
            if metadata.get("result_supplied"):
                self._mcp_result(metadata.get("result"))
            else:
                print("  Result: no result supplied", file=sys.stderr)
        sys.stderr.flush()

    def event(self, name, **metadata):
        with self.lock:
            stream_key = (metadata.get("turn_id"), metadata.get("item_id"))
            if name == "assistant.text_delta":
                if self.active_stream != stream_key:
                    self._close_stream()
                    self._heading(f"Turn {metadata['turn_number']}: Assistant response")
                    print("--- Live response ---", file=sys.stderr)
                    self.active_stream = stream_key
                    self.stream_ends_with_newline = True
                delta = metadata["delta"]
                self.streamed.setdefault(stream_key, []).append(delta)
                sys.stderr.write(delta)
                sys.stderr.flush()
                if delta:
                    self.stream_ends_with_newline = delta.endswith("\n")
                return

            self._close_stream()
            if name == "token_usage.updated":
                self._heading(f"Token usage update: {metadata.get('summary', '')}")
                return
            titles = {
                "request.received": "Request received",
                "request.validated": "Request validated",
                "source.staged": "Source staged",
                "app_server.starting": "Starting app-server",
                "app_server.ready": "App-server ready",
                "app_server.stopped": "App-server stopped",
                "thread.ready": "Thread ready",
                "runtime.configured": "Extraction runtime configured",
                "turn.started": "Starting",
                "turn.completed": "Completed",
                "turn.failed": "Failed",
                "workflow.completed": "Extraction complete",
                "workflow.failed": "Extraction failed",
                "assistant.response.completed": "Assistant response received",
                "token_usage.updated": "Token usage update",
                "usage.summary": "Token usage summary",
                "result.written": "Result written to stdout",
                "validation.markdown": "Markdown validation",
                "validation.document_data": "Structured data validation",
                "cost.basis": "Cost estimate basis",
                "model.rerouted": "Model rerouted",
                "tool.command.started": "Command started",
                "tool.command.completed": "Command completed",
                "tool.image_view.started": "Image view started",
                "tool.image_view.completed": "Image view completed",
                "mcp.call.started": "MCP tool call started",
                "mcp.call.progress": "MCP tool call progress",
                "mcp.call.completed": "MCP tool call completed",
                "mcp.call.failed": "MCP tool call failed",
            }
            title = titles.get(name, self._label(name))
            turn_number = metadata.pop("turn_number", None)
            if turn_number is not None:
                title = f"Turn {turn_number}: {title}"
            if name in {"mcp.call.started", "mcp.call.progress", "mcp.call.completed", "mcp.call.failed"}:
                self._mcp_event(name, title, metadata)
                sys.stderr.flush()
                return
            if name == "assistant.response.completed":
                streamed = self.streamed.pop(stream_key, [])
                live_text = "".join(streamed)
                final_text = metadata.pop("text", "")
                if not streamed:
                    metadata["text"] = final_text
                elif final_text.startswith(live_text) and final_text != live_text:
                    metadata["remaining_text"] = final_text[len(live_text):]
                elif final_text != live_text:
                    metadata["stream_note"] = "Final response differed from the live text already shown; full final follows."
                    metadata["text"] = final_text
            self._heading(title)
            for key, value in metadata.items():
                self._field(key, value)
            sys.stderr.flush()


LOGGER = WorkflowLogger()


def fail(message):
    LOGGER.event("workflow.failed", error=message)
    raise SystemExit(1)


def read_request():
    try:
        request = json.load(sys.stdin)
        blob_value = request.get("blob") if isinstance(request, dict) else None
        blob_metadata = (
            {
                key: blob_value[key]
                for key in ("media_type", "byte_size")
                if key in blob_value
            }
            if isinstance(blob_value, dict)
            else {"type": type(blob_value).__name__}
        )
        input_model = request.get("model") if isinstance(request, dict) else None
        model_context = (
            {
                key: input_model[key]
                for key in ("name", "reasoning_effort")
                if key in input_model
            }
            if isinstance(input_model, dict)
            else None
        )
        LOGGER.event(
            "request.received",
            source=blob_metadata,
            import_context=request.get("import_context", {}) if isinstance(request, dict) else None,
            model=model_context,
        )
        blob = request["blob"]
        data = base64.b64decode(blob["content_base64"], validate=True)
        model = request["model"]
    except (ValueError, TypeError, KeyError, binascii.Error) as error:
        fail(f"invalid v1 request: {error}")
    if request.get("protocol_version") != 1:
        fail("unsupported protocol version")
    if not isinstance(blob, dict):
        fail("blob must be an object")
    if not isinstance(model, dict):
        fail("model must be an object")
    if blob.get("byte_size") != len(data):
        fail("Blob byte size does not match request")
    if blob.get("blobref") != "sha256-" + hashlib.sha256(data).hexdigest():
        fail("Blobref does not match request bytes")
    if model.get("provider") != "codex_app_server":
        fail("model.provider must be codex_app_server")
    if not isinstance(model.get("name"), str) or not model["name"]:
        fail("model.name is required")
    if model.get("reasoning_effort") not in ("minimal", "low", "medium", "high", "xhigh"):
        fail("model.reasoning_effort is invalid")
    command = model.get("command")
    if (
        not isinstance(command, list)
        or not command
        or not all(isinstance(part, str) and part for part in command)
        or not os.path.isabs(command[0])
    ):
        fail("model.command must be an argv array with an absolute executable path")
    LOGGER.event("request.validated", outcome="success")
    return blob, data, model, request.get("import_context", {})


def staged_filename(import_context, media_type):
    supplied = import_context.get("original_filename") if isinstance(import_context, dict) else None
    if not isinstance(supplied, str) or not supplied.strip():
        default_names = {
            "application/pdf": "document.pdf",
            "image/png": "document.png",
            "image/jpeg": "document.jpg",
            "application/json": "document.json",
            "text/plain": "document.txt",
            "text/markdown": "document.md",
        }
        supplied = default_names.get(media_type, "document")
    filename = Path(supplied.replace("\\", "/")).name.replace("\x00", "_")
    if filename in ("", ".", ".."):
        filename = "document"
    if len(filename.encode("utf-8")) > 220:
        suffix = Path(filename).suffix[:32]
        stem = Path(filename).stem
        filename = stem[: max(1, 180 - len(suffix))] + suffix
    return filename


def format_import_context(import_context):
    if not isinstance(import_context, dict) or not import_context:
        return "No import context was provided."
    return json.dumps(import_context, ensure_ascii=False, sort_keys=True)


def app_server_command(command, config_overrides=()):
    disabled = [part for feature in DISABLED_CODEX_FEATURES for part in ("--disable", feature)]
    enabled = [part for feature in ENABLED_CODEX_FEATURES for part in ("--enable", feature)]
    overrides = (
        "tools.web_search=false",
        "project_doc_max_bytes=0",
        'developer_instructions=""',
        'instructions=""',
        *config_overrides,
    )
    return [*command, *enabled, *disabled, *[part for value in overrides for part in ("-c", value)]]


def parse_usage_breakdown(breakdown):
    if not isinstance(breakdown, dict):
        return None
    names = {
        "input_tokens": "inputTokens",
        "cached_input_tokens": "cachedInputTokens",
        "cache_write_input_tokens": "cacheWriteInputTokens",
        "output_tokens": "outputTokens",
        "reasoning_output_tokens": "reasoningOutputTokens",
        "total_tokens": "totalTokens",
    }
    usage = {}
    for key, wire_name in names.items():
        value = breakdown.get(wire_name, 0 if key == "cache_write_input_tokens" else None)
        if isinstance(value, bool) or not isinstance(value, int) or value < 0:
            return None
        usage[key] = value
    return usage


def estimate_api_cost(usage, model_name):
    """Estimate gpt-6-luna Standard short-context API-equivalent cost."""
    if model_name != "gpt-6-luna" or not isinstance(usage, dict):
        return None
    required = (
        "input_tokens",
        "cached_input_tokens",
        "output_tokens",
        "reasoning_output_tokens",
        "total_tokens",
    )
    if any(
        isinstance(usage.get(key), bool)
        or not isinstance(usage.get(key), int)
        or usage[key] < 0
        for key in required
    ):
        return None
    cache_write_tokens = usage.get("cache_write_input_tokens", 0)
    if (
        isinstance(cache_write_tokens, bool)
        or not isinstance(cache_write_tokens, int)
        or cache_write_tokens < 0
    ):
        return None
    regular_input = (
        usage["input_tokens"] - usage["cached_input_tokens"] - cache_write_tokens
    )
    if regular_input < 0:
        return None
    amount = (
        regular_input * LUNA_INPUT_RATE
        + usage["cached_input_tokens"] * LUNA_CACHED_INPUT_RATE
        + cache_write_tokens * LUNA_CACHE_WRITE_RATE
        + usage["output_tokens"] * LUNA_OUTPUT_RATE
    ) / 1_000_000
    return {
        "amount_usd": round(amount, 9),
        "basis": "standard_api_equivalent_short_context",
        "pricing_date": PRICING_DATE,
        "pricing_url": PRICING_URL,
    }


def cost_unavailable_reason(usage, model_name):
    if not model_name:
        return "model_unknown"
    if model_name != "gpt-6-luna":
        return "no_rate_for_model"
    if not isinstance(usage, dict):
        return "usage_missing"
    if estimate_api_cost(usage, model_name) is not None:
        return None
    token_fields = (
        "input_tokens",
        "cached_input_tokens",
        "output_tokens",
        "reasoning_output_tokens",
        "total_tokens",
    )
    cache_write_tokens = usage.get("cache_write_input_tokens", 0)
    if any(
        isinstance(usage.get(key), bool)
        or not isinstance(usage.get(key), int)
        or usage[key] < 0
        for key in token_fields
    ) or (
        isinstance(cache_write_tokens, bool)
        or not isinstance(cache_write_tokens, int)
        or cache_write_tokens < 0
    ):
        return "invalid_usage_breakdown"
    if usage["cached_input_tokens"] + cache_write_tokens > usage["input_tokens"]:
        return "cache_tokens_exceed_input_tokens"
    return "invalid_usage_breakdown"


def usage_from_message(message):
    token_usage = message.get("params", {}).get("tokenUsage") or {}
    return parse_usage_breakdown(token_usage.get("total"))


def usage_delta(before, after):
    if after is None:
        return None
    if before is None:
        before = {key: 0 for key in after}
    delta = {key: value - before[key] for key, value in after.items()}
    return None if any(value < 0 for value in delta.values()) else delta


class AppServer:
    def __init__(self, command, directory=None, config_overrides=(), purpose="Extraction"):
        command = app_server_command(command, config_overrides)
        self.started_at = time.monotonic()
        LOGGER.event("app_server.starting", purpose=purpose)
        self.process = subprocess.Popen(
            command,
            cwd=directory,
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            bufsize=1,
        )
        self.messages = queue.Queue()
        self.pending = []
        self.latest_usage = None
        self.usage_updates = 0
        self.usage_messages = 0
        self.usage_snapshots = set()
        self.actual_model = None
        self.turn_models = {}
        self.turn_model_observations = {}
        self.turn_model_unavailable_reasons = {}
        self.stdout_thread = threading.Thread(target=self._read_stdout, daemon=True)
        self.stderr_thread = threading.Thread(target=self._read_stderr, daemon=True)
        self.stdout_thread.start()
        self.stderr_thread.start()
        LOGGER.event("app_server.ready")

    def _read_stdout(self):
        for line in self.process.stdout:
            self.messages.put(line)
        self.messages.put(None)

    def _read_stderr(self):
        for line in self.process.stderr:
            LOGGER.event("app_server.stderr", text=line.rstrip("\r\n"))

    def send(self, message):
        self.process.stdin.write(json.dumps(message, ensure_ascii=False) + "\n")
        self.process.stdin.flush()

    def receive(self, deadline):
        try:
            line = self.messages.get(timeout=max(0, deadline - time.monotonic()))
        except queue.Empty:
            raise RuntimeError("app-server timed out") from None
        if line is None:
            raise RuntimeError("app-server closed its output")
        try:
            message = json.loads(line)
        except json.JSONDecodeError as error:
            raise RuntimeError(f"invalid app-server JSON: {error}") from error
        method = message.get("method")
        params = message.get("params", {})
        if method == "thread/tokenUsage/updated":
            token_usage = params.get("tokenUsage") or {}
            usage = usage_from_message(message)
            self.usage_messages += 1
            last_raw = token_usage.get("last")
            snapshot_key = json.dumps(
                [params.get("turnId"), last_raw, token_usage.get("total")],
                ensure_ascii=False,
                sort_keys=True,
            )
            if snapshot_key not in self.usage_snapshots:
                self.usage_snapshots.add(snapshot_key)
                last_usage = parse_usage_breakdown(last_raw)
                summary = (
                    "last model call: " + WorkflowLogger._format_usage(last_usage)
                    if last_usage is not None
                    else "last model call counts unavailable (missing or invalid breakdown)"
                )
                LOGGER.event("token_usage.updated", summary=summary)
            if usage is not None:
                self.latest_usage = usage
                self.usage_updates += 1
        elif method == "model/rerouted":
            from_model = params.get("fromModel") or self.actual_model
            to_model = params.get("toModel") or self.actual_model
            self.actual_model = to_model
            turn_id = params.get("turnId")
            if turn_id is not None:
                observed = self.turn_model_observations.setdefault(turn_id, set())
                if isinstance(from_model, str) and from_model:
                    observed.add(from_model)
                if isinstance(to_model, str) and to_model:
                    observed.add(to_model)
            LOGGER.event(
                "model.rerouted",
                from_model=from_model,
                to_model=self.actual_model,
            )
        elif method in ("warning", "configWarning", "error"):
            LOGGER.event(f"app_server.{method}", params=params)
        return message

    def response(self, request_id, deadline):
        while True:
            message = self.receive(deadline)
            if message.get("id") == request_id:
                if "error" in message:
                    LOGGER.event(
                        "app_server.error",
                        error=message["error"],
                    )
                    raise RuntimeError(f"app-server request failed: {message['error']}")
                return message["result"]
            if "id" in message and "method" in message:
                log_unsupported_request(message)
                self.send({
                    "id": message["id"],
                    "error": {
                        "code": -32601,
                        "message": "This client does not support server requests",
                    },
                })
            else:
                self.pending.append(message)

    def next_event(self, deadline):
        return self.pending.pop(0) if self.pending else self.receive(deadline)

    def close(self):
        if self.process.poll() is None:
            self.process.terminate()
            try:
                self.process.wait(timeout=3)
            except subprocess.TimeoutExpired:
                self.process.kill()
                self.process.wait()
        self.stderr_thread.join(timeout=1)
        LOGGER.event(
            "app_server.stopped",
            returncode=self.process.poll(),
            elapsed_seconds=f"{time.monotonic() - self.started_at:.1f}s",
        )


def log_unsupported_request(message):
    LOGGER.event(
        "app_server.unsupported_request",
        method=message.get("method"),
    )


def initialize_server(server):
    server.send({
        "id": 1,
        "method": "initialize",
        "params": {
            "clientInfo": {
                "name": "memoryd_extractor",
                "title": "memoryd document extractor",
                "version": PLUGIN_VERSION,
            },
            "capabilities": {"experimentalApi": True},
        },
    })
    server.response(1, time.monotonic() + TURN_TIMEOUT_SECONDS)
    server.send({"method": "initialized", "params": {}})


def list_runtime_skills(server, directory):
    server.send({
        "id": "extractor-skills",
        "method": "skills/list",
        "params": {"cwds": [directory], "forceReload": True},
    })
    result = server.response("extractor-skills", time.monotonic() + TURN_TIMEOUT_SECONDS)
    groups = result.get("data")
    if not isinstance(groups, list) or not groups:
        raise RuntimeError("Codex did not return a skills inventory; extraction stopped before model turns")
    skills = {}
    for group in groups:
        if group.get("errors"):
            raise RuntimeError("Codex could not discover all skills; extraction stopped before model turns")
        entries = group.get("skills")
        if not isinstance(entries, list):
            raise RuntimeError("Codex returned an invalid skills inventory")
        for skill in entries:
            path = skill.get("path")
            if not isinstance(path, str) or not os.path.isabs(path):
                raise RuntimeError("Codex returned a skill without an absolute path")
            skills[path] = skills.get(path, False) or skill.get("enabled") is not False
    return skills


def open_extraction_server(command, directory):
    # Discover paths without putting the skill catalog into a model conversation.
    discovery = AppServer(command, directory, purpose="Discovering skill and MCP exclusions")
    try:
        initialize_server(discovery)
        discovery.send({
            "id": "extractor-config",
            "method": "config/read",
            "params": {"cwd": directory, "includeLayers": True},
        })
        result = discovery.response("extractor-config", time.monotonic() + TURN_TIMEOUT_SECONDS)
        config = result.get("config")
        if not isinstance(config, dict):
            raise RuntimeError("Codex did not return its effective configuration")
        # Keep config private: it can contain credentials and unrelated user settings.
        mcp_servers = config.get("mcp_servers", {})
        if not isinstance(mcp_servers, dict):
            raise RuntimeError("Codex returned an invalid MCP configuration")
        skills = list_runtime_skills(discovery, directory)
    finally:
        discovery.close()

    skill_entries = ",".join(
        f"{{path={json.dumps(path, ensure_ascii=False)},enabled=false}}"
        for path in sorted(skills)
    )
    # CLI override paths split on dots; quote server names inside a TOML value instead.
    mcp_entries = ",".join(
        f"{json.dumps(name, ensure_ascii=False)}={{enabled=false}}"
        for name in sorted(mcp_servers)
    )
    overrides = [f"skills.config=[{skill_entries}]", f"mcp_servers={{{mcp_entries}}}"]
    server = AppServer(command, directory, overrides)
    try:
        initialize_server(server)
        if any(list_runtime_skills(server, directory).values()):
            raise RuntimeError("Codex still exposes enabled skills; extraction stopped before model turns")
        LOGGER.event(
            "runtime.configured",
            file_tools=["shell execution", "image viewing"],
            skills=f"0 enabled; {len(skills)} discovered paths disabled",
            excluded_mcp_servers=sorted(mcp_servers),
            extra_instructions="disabled",
        )
        return server
    except BaseException:
        server.close()
        raise


def start_thread(server, model, directory, request_id):
    server.send({
        "id": request_id,
        "method": "thread/start",
        "params": {
            "model": model["name"],
            "cwd": directory,
            "approvalPolicy": "never",
            "sandbox": "workspace-write",
            "ephemeral": True,
            "serviceName": "memoryd_extractor",
            "baseInstructions": "",
            "developerInstructions": "",
        },
    })
    thread_id = server.response(request_id, time.monotonic() + TURN_TIMEOUT_SECONDS)["thread"]["id"]
    LOGGER.event("thread.ready")
    return thread_id


def run_turn(
    server,
    thread_id,
    model,
    directory,
    request_id,
    prompt,
    turn_number,
    stage_name,
    output_schema=None,
    local_document=None,
):
    turn_started_at = time.monotonic()
    usage_before = server.latest_usage
    updates_before = server.usage_updates
    messages_before = server.usage_messages
    inputs = [{"type": "text", "text": prompt}]
    if local_document is not None:
        inputs.append({"type": "text", "text": local_document})
    params = {
        "threadId": thread_id,
        "model": model["name"],
        "effort": model["reasoning_effort"],
        "approvalPolicy": "never",
        "sandboxPolicy": {
            "type": "workspaceWrite",
            "writableRoots": [directory],
            "networkAccess": False,
        },
        "input": inputs,
    }
    if output_schema is not None:
        params["outputSchema"] = output_schema
    LOGGER.event(
        "turn.started",
        turn_number=turn_number,
        stage_name=stage_name,
        prompt=prompt,
        output_schema=output_schema,
        local_document=local_document,
    )
    try:
        server.send({"id": request_id, "method": "turn/start", "params": params})
        turn_id = server.response(request_id, time.monotonic() + TURN_TIMEOUT_SECONDS)["turn"]["id"]
        observed_models = server.turn_model_observations.setdefault(turn_id, set())
        observed_models.add(model["name"])
        if server.actual_model:
            observed_models.add(server.actual_model)
    except Exception as error:
        LOGGER.event(
            "turn.failed",
            turn_number=turn_number,
            status="failed_before_acceptance",
            elapsed_seconds=f"{time.monotonic() - turn_started_at:.1f}s",
            error=str(error),
        )
        raise
    finals = []
    legacy = []
    mcp_calls = {}
    command_outputs = {}
    deadline = time.monotonic() + TURN_TIMEOUT_SECONDS
    while True:
        try:
            message = server.next_event(deadline)
        except Exception as error:
            LOGGER.event(
                "turn.failed",
                turn_number=turn_number,
                status="failed_after_acceptance",
                elapsed_seconds=f"{time.monotonic() - turn_started_at:.1f}s",
                error=str(error),
            )
            raise
        if "id" in message and "method" in message:
            log_unsupported_request(message)
            server.send({
                "id": message["id"],
                "error": {
                    "code": -32601,
                    "message": "This client does not support server requests",
                },
            })
            continue
        params = message.get("params", {})
        method = message.get("method")
        if method == "item/agentMessage/delta" and params.get("turnId") == turn_id:
            delta = params.get("delta")
            if isinstance(delta, str):
                LOGGER.event(
                    "assistant.text_delta",
                    turn_number=turn_number,
                    stage_name=stage_name,
                    turn_id=turn_id,
                    item_id=params.get("itemId"),
                    delta=delta,
                )
        if method == "item/commandExecution/outputDelta" and params.get("turnId") == turn_id:
            delta = params.get("delta")
            item_id = params.get("itemId")
            if isinstance(delta, str) and item_id is not None:
                command_outputs.setdefault(item_id, []).append(delta)
        if method == "item/completed" and params.get("turnId") == turn_id:
            item = params.get("item", {})
            if item.get("type") == "mcpToolCall":
                item_id = item.get("id")
                started = mcp_calls.pop(item_id, {})
                call = {
                    **started,
                    "server": item.get("server", item.get("serverName", started.get("server"))),
                    "tool": item.get("tool", item.get("toolName", started.get("tool"))),
                    "status": item.get("status"),
                    "arguments": item.get("arguments", started.get("arguments")),
                    "error": item.get("error"),
                    "result": item.get("result"),
                    "result_supplied": "result" in item and item.get("result") is not None,
                    "item_id": item_id,
                    "turn_number": turn_number,
                }
                status = str(call.get("status") or "").lower()
                event_name = (
                    "mcp.call.failed"
                    if call.get("error") is not None or status in {"failed", "error"}
                    else "mcp.call.completed"
                )
                LOGGER.event(event_name, **call)
            elif item.get("type") == "commandExecution":
                item_id = item.get("id")
                delta_output = "".join(command_outputs.pop(item_id, []))
                aggregated_output = item.get("aggregatedOutput")
                if not isinstance(aggregated_output, str) or (not aggregated_output and delta_output):
                    aggregated_output = delta_output
                LOGGER.event(
                    "tool.command.completed",
                    turn_number=turn_number,
                    command=item.get("command"),
                    cwd=item.get("cwd"),
                    status=item.get("status"),
                    exit_code=item.get("exitCode"),
                    duration_ms=item.get("durationMs"),
                    command_output=aggregated_output,
                    error=item.get("error"),
                )
            elif item.get("type") == "imageView":
                LOGGER.event(
                    "tool.image_view.completed",
                    turn_number=turn_number,
                    path=item.get("path"),
                )
            elif item.get("type") == "agentMessage" and isinstance(item.get("text"), str):
                phase = item.get("phase")
                LOGGER.event(
                    "assistant.response.completed",
                    turn_number=turn_number,
                    turn_id=turn_id,
                    item_id=item.get("id"),
                    phase=phase,
                    text=item["text"],
                )
                if item.get("phase") == "final_answer":
                    finals.append(item["text"])
                elif item.get("phase") is None:
                    legacy.append(item["text"])
        elif method == "item/started" and params.get("turnId") == turn_id:
            item = params.get("item", {})
            if item.get("type") == "mcpToolCall":
                item_id = item.get("id")
                call = {
                    "server": item.get("server", item.get("serverName")),
                    "tool": item.get("tool", item.get("toolName")),
                }
                if "arguments" in item:
                    call["arguments"] = item["arguments"]
                if item_id is not None:
                    mcp_calls[item_id] = call
                LOGGER.event(
                    "mcp.call.started",
                    turn_number=turn_number,
                    item_id=item_id,
                    **call,
                )
            elif item.get("type") == "commandExecution":
                LOGGER.event(
                    "tool.command.started",
                    turn_number=turn_number,
                    command=item.get("command"),
                    cwd=item.get("cwd"),
                )
            elif item.get("type") == "imageView":
                LOGGER.event(
                    "tool.image_view.started",
                    turn_number=turn_number,
                    path=item.get("path"),
                )
        elif method == "item/mcpToolCall/progress" and params.get("turnId") == turn_id:
            item_id = params.get("itemId")
            call = mcp_calls.get(item_id, {})
            LOGGER.event(
                "mcp.call.progress",
                turn_number=turn_number,
                item_id=item_id,
                server=call.get("server"),
                tool=call.get("tool"),
                message=params.get("message"),
            )
        if (
            message.get("method") == "turn/completed"
            and params.get("turn", {}).get("id") == turn_id
        ):
            turn = params["turn"]
            turn_status = turn.get("status")
            turn_elapsed = f"{time.monotonic() - turn_started_at:.1f}s"
            if turn_status != "completed":
                LOGGER.event(
                    "turn.failed",
                    turn_number=turn_number,
                    stage_name=stage_name,
                    status=turn_status,
                    elapsed_seconds=turn_elapsed,
                    error=turn.get("error"),
                    usage_missing_reason="turn_did_not_complete",
                    cost_unavailable_reason="turn_did_not_complete",
                )
                raise RuntimeError(
                    f"model turn {turn_number} {turn_status}: {turn.get('error')}"
                )
            output = finals or legacy
            if not output or not output[-1].strip():
                LOGGER.event(
                    "turn.failed",
                    turn_number=turn_number,
                    stage_name=stage_name,
                    status=turn_status,
                    elapsed_seconds=turn_elapsed,
                    usage_missing_reason="no_valid_final_assistant_message",
                    cost_unavailable_reason="no_valid_final_assistant_message",
                )
                raise RuntimeError(f"model turn {turn_number} returned no final response")
            turn_usage = None
            usage_missing_reason = None
            if server.usage_updates > updates_before:
                turn_usage = usage_delta(usage_before, server.latest_usage)
                if turn_usage is None:
                    usage_missing_reason = "cumulative_snapshot_decreased_or_incompatible"
            else:
                usage_missing_reason = (
                    "no_valid_total_snapshot_received"
                    if server.usage_messages > messages_before
                    else "no_token_usage_updates_received"
                )
            final_output = output[-1].strip()
            observed_models = server.turn_model_observations.get(turn_id, set())
            if len(observed_models) > 1:
                turn_model = None
                turn_cost_unavailable_reason = "mixed_models_within_turn"
            else:
                turn_model = next(iter(observed_models), server.actual_model)
                turn_cost_unavailable_reason = cost_unavailable_reason(turn_usage, turn_model)
            server.turn_models[turn_number] = turn_model
            server.turn_model_unavailable_reasons[turn_number] = turn_cost_unavailable_reason
            cost_estimate = estimate_api_cost(turn_usage, turn_model)
            LOGGER.event(
                "turn.completed",
                turn_number=turn_number,
                stage_name=stage_name,
                status=turn_status,
                elapsed_seconds=turn_elapsed,
                usage=turn_usage,
                usage_missing_reason=usage_missing_reason,
                cost_estimate=cost_estimate,
                cost_unavailable_reason=turn_cost_unavailable_reason,
            )
            return final_output, turn_usage


def validate_markdown(value):
    if not value.startswith("# Description"):
        raise ValueError("first model turn must begin with '# Description'")
    headings = re.findall(r"(?m)^# (Description|Summary|Structured data)\s*$", value)
    if headings != ["Description", "Summary", "Structured data"]:
        raise ValueError("first model turn must contain the three requested headings in order")
    return value.rstrip() + "\n"


def validate_document_data(value):
    categories = {"facts", "events", "references", "signals", "uncertainties"}
    if not isinstance(value, dict) or set(value) != categories:
        raise ValueError(
            "document_data must contain exactly facts, events, references, signals, and uncertainties"
        )
    row_positions = {
        "facts": ("string", "string", "string"),
        "events": ("string", "nullable_string", "string", "string"),
        "references": ("string", "string", "nullable_string", "nullable_string", "string"),
        "signals": ("string", "string", "string"),
    }
    for category, rows in value.items():
        if not isinstance(rows, list):
            raise ValueError(f"document_data.{category} must be an array")
        if category == "uncertainties":
            if any(not isinstance(item, str) for item in rows):
                raise ValueError("document_data.uncertainties entries must be strings")
            continue
        expected_types = row_positions[category]
        for index, row in enumerate(rows):
            path = f"document_data.{category}[{index}]"
            if not isinstance(row, list) or len(row) != len(expected_types):
                raise ValueError(f"{path} must contain exactly {len(expected_types)} positions")
            for position, (item, expected_type) in enumerate(zip(row, expected_types)):
                if not isinstance(item, str) and not (expected_type == "nullable_string" and item is None):
                    raise ValueError(
                        f"{path}[{position}] must be {expected_type.replace('_', ' ')}"
                    )
    return value


def extract(data, blob, model, import_context):
    filename = staged_filename(import_context, blob.get("media_type", ""))
    with tempfile.TemporaryDirectory(prefix="memoryd-extractor-") as directory:
        server = open_extraction_server(model["command"], directory)
        server.actual_model = model["name"]
        try:
            document_path = Path(directory) / filename
            document_path.write_bytes(data)
            LOGGER.event(
                "source.staged",
                filename=filename,
                path=str(document_path.resolve()),
                media_type=blob.get("media_type"),
            )
            thread_id = start_thread(server, model, directory, 2)
            context = format_import_context(import_context)
            markdown_prompt = MARKDOWN_PROMPT.format(filename=filename, import_context=context)
            local_document_reference = f"LOCAL DOCUMENT\n{document_path.resolve()}"
            markdown_output, usage_one = run_turn(
                server,
                thread_id,
                model,
                directory,
                3,
                markdown_prompt,
                turn_number=1,
                stage_name="markdown_extraction",
                local_document=local_document_reference,
            )
            try:
                markdown = validate_markdown(markdown_output)
            except ValueError as error:
                LOGGER.event(
                    "validation.markdown",
                    outcome="failed",
                    category="heading_structure",
                    error=str(error),
                )
                raise
            LOGGER.event(
                "validation.markdown",
                outcome="success",
            )
            json_output, usage_two = run_turn(
                server,
                thread_id,
                model,
                directory,
                4,
                JSON_PROMPT,
                turn_number=2,
                stage_name="structured_data_extraction",
                output_schema=DOCUMENT_DATA_SCHEMA,
                local_document=local_document_reference,
            )
            try:
                decoded_document_data = json.loads(json_output)
            except json.JSONDecodeError as error:
                LOGGER.event(
                    "validation.document_data",
                    outcome="failed",
                    category="json_syntax",
                    error=str(error),
                )
                raise RuntimeError(
                    f"second model turn returned invalid document_data JSON: {error}"
                ) from error
            try:
                document_data = validate_document_data(decoded_document_data)
            except ValueError as error:
                error_text = str(error)
                category = next(
                    (name for name in ("facts", "events", "references", "signals", "uncertainties")
                     if f"document_data.{name}" in error_text),
                    "top_level_shape",
                )
                LOGGER.event(
                    "validation.document_data",
                    outcome="failed",
                    category=category,
                    error=error_text,
                )
                raise RuntimeError(
                    f"second model turn returned invalid document_data JSON: {error}"
                ) from error
            LOGGER.event(
                "validation.document_data",
                outcome="success",
            )
            usage = server.latest_usage
            turn_usage = None
            if usage is not None and usage_one is not None and usage_two is not None:
                sum_usage = {key: usage_one[key] + usage_two[key] for key in usage}
                if sum_usage == usage:
                    turn_usage = [
                        {"turn": 1, "usage": usage_one},
                        {"turn": 2, "usage": usage_two},
                    ]
            cumulative_missing_reason = None
            if usage is None:
                cumulative_missing_reason = (
                    "no_token_usage_updates_received"
                    if server.usage_messages == 0
                    else "no_valid_cumulative_total_snapshot_received"
                )
            turn_models = list(server.turn_models.values())
            if usage is None:
                cost_model = None
                cumulative_cost_unavailable_reason = "usage_missing"
            elif any(turn_model is None for turn_model in turn_models):
                cost_model = None
                cumulative_cost_unavailable_reason = next(
                    (
                        reason
                        for reason in server.turn_model_unavailable_reasons.values()
                        if reason == "mixed_models_within_turn"
                    ),
                    "model_unknown_for_turn",
                )
            elif len(set(turn_models)) > 1:
                cost_model = None
                cumulative_cost_unavailable_reason = "mixed_models_across_turns"
            else:
                cost_model = turn_models[-1] if turn_models else server.actual_model
                cumulative_cost_unavailable_reason = cost_unavailable_reason(usage, cost_model)
            cost_estimate = estimate_api_cost(usage, cost_model)
            LOGGER.event(
                "usage.summary",
                usage=usage,
                usage_missing_reason=cumulative_missing_reason,
                cost_estimate=cost_estimate,
                cost_unavailable_reason=cumulative_cost_unavailable_reason,
            )
            return markdown, document_data, usage, turn_usage, server.actual_model, filename, cost_estimate
        finally:
            server.close()


def main():
    try:
        blob, data, model, import_context = read_request()
        LOGGER.event(
            "cost.basis",
            details=(
                "gpt-6-luna Standard short-context rates per 1M tokens: input $0.10, "
                "cached input $0.01, cache-write input $0.125, output $0.50. "
                "Assumes each request is short-context; no long tier is inferred from "
                "summed calls. API-equivalent estimate, not a subscription bill."
            ),
            pricing_date=PRICING_DATE,
            pricing_url=PRICING_URL,
        )
        try:
            markdown, document_data, usage, turn_usage, actual_model, filename, cost_estimate = extract(
                data, blob, model, import_context
            )
        except (OSError, RuntimeError, KeyError, ValueError, BrokenPipeError) as error:
            fail(f"document extraction failed: {error}")
        provenance = {
            "method": "Codex app-server document extraction",
            "model": actual_model,
            "reasoning_effort": model["reasoning_effort"],
            "source_filename": filename,
            "source_media_type": blob.get("media_type"),
            "thread_mode": "shared",
        }
        result = {
            "protocol_version": 1,
            "plugin_version": PLUGIN_VERSION,
            "artifacts": [
                {
                    "kind": "document_md",
                    "media_type": "text/markdown",
                    "content": markdown,
                    "provenance": {
                        **provenance,
                        "turn": 1,
                        "analysis": "description_summary_and_structured_data",
                    },
                },
                {
                    "kind": "document_data",
                    "media_type": "application/json",
                    "content": json.dumps(document_data, ensure_ascii=False, indent=2) + "\n",
                    "provenance": {
                        **provenance,
                        "turn": 2,
                        "schema": "memoryd.document_data.v2",
                    },
                },
            ],
            "warnings": [],
        }
        if usage is not None:
            result["usage"] = usage
        if turn_usage is not None:
            result["turn_usage"] = turn_usage
        if cost_estimate is not None:
            result["cost_estimate"] = cost_estimate
        json.dump(result, sys.stdout, ensure_ascii=False)
        sys.stdout.write("\n")
        sys.stdout.flush()
        LOGGER.event(
            "result.written",
            artifacts=[artifact["kind"] for artifact in result["artifacts"]],
        )
        LOGGER.event("workflow.completed", status="success")
    except SystemExit:
        raise
    except Exception as error:
        LOGGER.event("workflow.failed", error=str(error))
        raise SystemExit(1) from error


if __name__ == "__main__":
    main()
