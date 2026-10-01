"""Exercise workflow diagnostics against a local app-server protocol fixture."""

import base64
from contextlib import redirect_stderr
import hashlib
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

import plugin


MARKDOWN = "# Description\nFixture document.\n\n# Summary\nA fixture.\n\n# Structured data\n| Key | Value |\n| --- | --- |\n| name | Fixture |\n"
DOCUMENT_DATA = {
    "facts": [["document_number", "A123", "Document No."]],
    "events": [["issued", None, "Document issued.", "Issue notice"]],
    "references": [["document", "B456", None, None, "See document B456"]],
    "signals": [["document_type", "invoice", "Invoice heading"]],
    "uncertainties": ["No issue date is stated."],
}

FAKE_SERVER = '''
import json
import sys

config = CONFIG
turn_number = 0

def emit(value):
    print(json.dumps(value), flush=True)

for line in sys.stdin:
    request = json.loads(line)
    method = request.get("method")
    if method == "initialize":
        print("fixture app-server diagnostic", file=sys.stderr, flush=True)
        emit({"id": request["id"], "result": {}})
    elif method == "config/read":
        emit({"id": request["id"], "result": {"config": {"mcp_servers": {}}}})
    elif method == "skills/list":
        emit({"id": request["id"], "result": {"data": [{"cwd": request["params"]["cwds"][0], "skills": [], "errors": []}]}})
    elif method == "thread/start":
        emit({"id": request["id"], "result": {"thread": {"id": "fixture-thread"}}})
    elif method == "turn/start":
        turn_number += 1
        turn_id = f"fixture-turn-{turn_number}"
        emit({"id": request["id"], "result": {"turn": {"id": turn_id}}})
        emit({"method": "turn/started", "params": {"threadId": "fixture-thread", "turn": {"id": turn_id, "status": "inProgress"}}})
        if config["reroute"] and turn_number == 1:
            emit({"method": "model/rerouted", "params": {"threadId": "fixture-thread", "turnId": turn_id, "fromModel": "gpt-6-luna", "toModel": "gpt-6-sol"}})
        emit({"method": "item/started", "params": {"threadId": "fixture-thread", "turnId": turn_id, "item": {"id": f"tool-{turn_number}", "type": "commandExecution", "status": "inProgress", "command": "fixture command (not executed)"}}})
        emit({"method": "item/completed", "params": {"threadId": "fixture-thread", "turnId": turn_id, "item": {"id": f"tool-{turn_number}", "type": "commandExecution", "status": "completed", "command": "fixture command (not executed)"}}})
        if config["mcp"]:
            call = {"id": f"mcp-{turn_number}", "type": "mcpToolCall", "server": "fixture-mcp", "tool": "js", "status": "inProgress", "arguments": {"code": "console.log('fixture');\\nconsole.log('result');"}}
            emit({"method": "item/started", "params": {"threadId": "fixture-thread", "turnId": turn_id, "item": call}})
            emit({"method": "item/mcpToolCall/progress", "params": {"threadId": "fixture-thread", "turnId": turn_id, "itemId": call["id"], "message": "Reading fixture document"}})
            result = {"content": [{"type": "text", "text": "MCP fixture text\\nsecond line."}, {"type": "image", "data": "BINARY_PAYLOAD_SENTINEL", "mimeType": "image/png"}, {"type": "audio", "data": "BINARY_PAYLOAD_SENTINEL", "mimeType": "audio/wav"}, {"type": "resource", "resource": {"uri": "memory://fixture", "mimeType": "text/plain", "text": "Embedded fixture text"}}, {"type": "resource", "resource": {"uri": "memory://binary", "mimeType": "application/octet-stream", "blob": "BINARY_PAYLOAD_SENTINEL"}}], "structuredContent": {"source": "fixture", "rows": 2}}
            emit({"method": "item/completed", "params": {"threadId": "fixture-thread", "turnId": turn_id, "item": {**call, "status": "completed", "result": result}}})
            emit({"method": "item/completed", "params": {"threadId": "fixture-thread", "turnId": turn_id, "item": {**call, "id": f"mcp-error-{turn_number}", "status": "failed", "error": {"message": "fixture tool failure"}}}})
            emit({"method": "item/completed", "params": {"threadId": "fixture-thread", "turnId": turn_id, "item": {**call, "id": f"mcp-empty-{turn_number}", "status": "completed"}}})
        emit({"method": "item/completed", "params": {"threadId": "fixture-thread", "turnId": turn_id, "item": {"id": f"reasoning-{turn_number}", "type": "reasoning", "content": ["private reasoning fixture sentinel"]}}})
        emit({"method": "item/completed", "params": {"threadId": "fixture-thread", "turnId": turn_id, "item": {"id": f"commentary-{turn_number}", "type": "agentMessage", "phase": "commentary", "text": "Inspecting fixture."}}})
        text = config["markdown"] if turn_number == 1 else config["json"]
        item = {"id": f"message-{turn_number}", "type": "agentMessage", "phase": "final_answer", "text": text}
        emit({"method": "item/started", "params": {"threadId": "fixture-thread", "turnId": turn_id, "item": {**item, "text": ""}}})
        emit({"method": "item/agentMessage/delta", "params": {"threadId": "fixture-thread", "turnId": turn_id, "itemId": item["id"], "delta": text}})
        emit({"method": "item/completed", "params": {"threadId": "fixture-thread", "turnId": turn_id, "item": item}})
        if config["usage"]:
            call_number = 2 * turn_number - 1 if config["extra_usage"] else turn_number
            total = {"inputTokens": 100 * call_number, "cachedInputTokens": 20 * call_number, "cacheWriteInputTokens": 0, "outputTokens": 10 * call_number, "reasoningOutputTokens": 2 * call_number, "totalTokens": 110 * call_number}
            last = {key: value // call_number for key, value in total.items()}
            usage_event = {"method": "thread/tokenUsage/updated", "params": {"threadId": "fixture-thread", "turnId": turn_id, "tokenUsage": {"last": last, "total": total, "modelContextWindow": 200000}}}
            emit(usage_event)
            if config["duplicate_usage"]:
                emit(usage_event)
            if config["extra_usage"]:
                next_total = {key: value + last[key] for key, value in total.items()}
                emit({"method": "thread/tokenUsage/updated", "params": {"threadId": "fixture-thread", "turnId": turn_id, "tokenUsage": {"last": last, "total": next_total, "modelContextWindow": 200000}}})
        emit({"method": "turn/completed", "params": {"threadId": "fixture-thread", "turn": {"id": turn_id, "status": "completed"}}})
'''


class StartupTests(unittest.TestCase):
    @unittest.skipUnless(shutil.which("codex"), "Codex executable is required")
    def test_mcp_exclusions_disable_literal_names_and_preserve_transports(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory) / "codex-home"
            home.mkdir()
            (home / "config.toml").write_text(
                '[mcp_servers.computer-use]\ncommand = "python3"\n'
                '[mcp_servers."server.with.dot"]\ncommand = "python3"\n',
                encoding="utf-8",
            )
            with patch.dict(os.environ, {"CODEX_HOME": str(home)}), redirect_stderr(io.StringIO()):
                server = plugin.open_extraction_server(
                    [shutil.which("codex"), "app-server", "--stdio"], directory
                )
                try:
                    server.send({
                        "id": "verify-config",
                        "method": "config/read",
                        "params": {"cwd": directory},
                    })
                    result = server.response("verify-config", plugin.time.monotonic() + 30)
                    servers = result["config"]["mcp_servers"]
                    self.assertEqual(set(servers), {"computer-use", "server.with.dot"})
                    for settings in servers.values():
                        self.assertIs(settings["enabled"], False)
                        self.assertEqual(settings["command"], "python3")
                finally:
                    server.close()


class WorkflowLoggingTests(unittest.TestCase):
    def test_response_chunks_are_visible_before_message_completion(self):
        logger = plugin.WorkflowLogger()
        stream = io.StringIO()
        fields = {"turn_number": 1, "turn_id": "turn-1", "item_id": "message-1"}
        with redirect_stderr(stream):
            logger.event("assistant.text_delta", **fields, delta="Hello ")
            self.assertTrue(stream.getvalue().endswith("Hello "))
            logger.event("assistant.text_delta", **fields, delta="world\n")
            self.assertIn("Hello world\n", stream.getvalue())
            logger.event("assistant.response.completed", **fields, text="Hello world\n")
        self.assertEqual(stream.getvalue().count("Hello world"), 1)

    def run_plugin(self, *, json_output=None, usage=True, protocol_version=2,
                   mcp=False, duplicate_usage=False, extra_usage=False, reroute=False,
                   model_name="gpt-6-luna"):
        data = b"Fixture document bytes."
        with tempfile.TemporaryDirectory() as directory:
            server_path = Path(directory) / "fake_server.py"
            config = {
                "markdown": MARKDOWN,
                "json": json.dumps(DOCUMENT_DATA) if json_output is None else json_output,
                "usage": usage,
                "mcp": mcp,
                "duplicate_usage": duplicate_usage,
                "extra_usage": extra_usage,
                "reroute": reroute,
            }
            server_path.write_text(FAKE_SERVER.replace("CONFIG", repr(config)), encoding="utf-8")
            request = {
                "protocol_version": protocol_version,
                "blob": {
                    "blobref": "sha256-" + hashlib.sha256(data).hexdigest(),
                    "media_type": "text/plain",
                    "byte_size": len(data),
                    "content_base64": base64.b64encode(data).decode(),
                },
                "import_context": {"original_filename": "fixture.txt"},
                "model": {
                    "provider": "codex_app_server",
                    "name": model_name,
                    "reasoning_effort": "low",
                    "command": [sys.executable, str(server_path)],
                },
            }
            completed = subprocess.run(
                [sys.executable, str(Path(plugin.__file__).resolve())],
                input=json.dumps(request),
                capture_output=True,
                text=True,
                timeout=10,
                check=False,
            )
        return completed, request

    def test_two_turn_workflow_logs_readable_inputs_outputs_and_usage(self):
        completed, request = self.run_plugin()
        self.assertEqual(completed.returncode, 0, completed.stderr)
        result = json.loads(completed.stdout)
        self.assertEqual(result["artifacts"][0]["content"], MARKDOWN)
        self.assertEqual(json.loads(result["artifacts"][1]["content"]), DOCUMENT_DATA)
        self.assertEqual(result["statistics"]["usage"]["total_tokens"], 220)
        self.assertNotIn("turn_usage", result["statistics"])

        log = completed.stderr
        self.assertIn("[codex-extractor +", log)
        self.assertIn("Turn 1: Starting", log)
        self.assertIn("Turn 2: Starting", log)
        self.assertIn(json.dumps(plugin.DOCUMENT_DATA_SCHEMA, ensure_ascii=False, indent=2), log)
        self.assertIn("Inspecting fixture.", log)
        self.assertEqual(log.count(MARKDOWN), 1)
        self.assertIn("fixture-turn-1", log)
        self.assertIn("fixture-turn-2", log)
        self.assertIn("cached input=20", log)
        self.assertIn("total=110", log)
        self.assertIn("total=220", log)
        self.assertIn("fixture app-server diagnostic", log)
        self.assertIn("Result written to stdout", log)
        self.assertIn("Extraction complete", log)
        self.assertIn("fixture command (not executed)", log)
        self.assertNotIn("private reasoning fixture sentinel", log)
        self.assertNotIn("content_base64", log)
        self.assertNotIn(request["blob"]["content_base64"], log)

    def test_mcp_arguments_progress_results_and_errors_are_readable(self):
        completed, _ = self.run_plugin(mcp=True)
        self.assertEqual(completed.returncode, 0, completed.stderr)
        log = completed.stderr
        self.assertIn("fixture-mcp", log)
        self.assertIn("console.log('fixture');", log)
        self.assertIn("Reading fixture document", log)
        self.assertIn("MCP fixture text\nsecond line.", log)
        self.assertIn("Embedded fixture text", log)
        self.assertIn("image/png", log)
        self.assertIn("audio/wav", log)
        self.assertIn("fixture tool failure", log)
        self.assertNotIn("BINARY_PAYLOAD_SENTINEL", log)
        self.assertIn('"rows": 2', log)
        self.assertIn("no result supplied", log)

    def test_duplicate_usage_does_not_duplicate_accounting_or_logs(self):
        completed, _ = self.run_plugin(duplicate_usage=True)
        self.assertEqual(completed.returncode, 0, completed.stderr)
        result = json.loads(completed.stdout)
        self.assertEqual(result["statistics"]["usage"]["total_tokens"], 220)
        self.assertAlmostEqual(result["cost_estimate"]["amount_usd"], 0.0000264, places=9)
        self.assertNotIn("Raw last", completed.stderr)
        self.assertNotIn("Parsed thread total", completed.stderr)
        self.assertNotIn("Since previous snapshot", completed.stderr)
        self.assertEqual(completed.stderr.count("Token usage update"), 2)
        self.assertEqual(completed.stderr.count("API-equivalent cost: $0.000013200 USD"), 2)
        self.assertEqual(completed.stderr.count("API-equivalent cost: $0.000026400 USD"), 1)

    def test_cost_estimates_price_cache_writes_and_do_not_double_count_reasoning(self):
        usage = {
            "input_tokens": 100,
            "cached_input_tokens": 20,
            "cache_write_input_tokens": 10,
            "output_tokens": 10,
            "reasoning_output_tokens": 2,
            "total_tokens": 110,
        }
        estimate = plugin.estimate_api_cost(usage, "gpt-6-luna")
        self.assertAlmostEqual(estimate["amount_usd"], 0.00001345, places=9)
        self.assertIsNone(plugin.estimate_api_cost(usage, "unknown-model"))
        self.assertIsNone(plugin.estimate_api_cost(None, "gpt-6-luna"))
        self.assertIsNone(plugin.estimate_api_cost({**usage, "cached_input_tokens": 101}, "gpt-6-luna"))

    def test_cost_validation_defaults_cache_writes_and_rejects_boolean_counts(self):
        usage = {
            "input_tokens": 100,
            "cached_input_tokens": 20,
            "output_tokens": 10,
            "reasoning_output_tokens": 2,
            "total_tokens": 110,
        }
        estimate = plugin.estimate_api_cost(usage, "gpt-6-luna")
        self.assertAlmostEqual(estimate["amount_usd"], 0.0000132, places=9)
        self.assertIsNone(plugin.cost_unavailable_reason(usage, "gpt-6-luna"))

        boolean_count = {**usage, "input_tokens": True}
        self.assertIsNone(plugin.estimate_api_cost(boolean_count, "gpt-6-luna"))
        self.assertEqual(
            plugin.cost_unavailable_reason(boolean_count, "gpt-6-luna"),
            "invalid_usage_breakdown",
        )

        excess_cache = {
            **usage,
            "cached_input_tokens": 90,
            "cache_write_input_tokens": 11,
        }
        self.assertIsNone(plugin.estimate_api_cost(excess_cache, "gpt-6-luna"))
        self.assertEqual(
            plugin.cost_unavailable_reason(excess_cache, "gpt-6-luna"),
            "cache_tokens_exceed_input_tokens",
        )

    def test_equal_call_counts_with_growing_total_remain_visible(self):
        completed, _ = self.run_plugin(extra_usage=True)
        self.assertEqual(completed.returncode, 0, completed.stderr)
        result = json.loads(completed.stdout)
        self.assertEqual(result["statistics"]["usage"]["total_tokens"], 440)
        self.assertEqual(completed.stderr.count("Token usage update"), 4)
        self.assertAlmostEqual(result["cost_estimate"]["amount_usd"], 0.0000528, places=9)

    def test_unknown_model_does_not_fabricate_costs(self):
        completed, _ = self.run_plugin(model_name="unknown-model")
        self.assertEqual(completed.returncode, 0, completed.stderr)
        self.assertNotIn("cost_estimate", json.loads(completed.stdout))
        self.assertIn("unavailable", completed.stderr.lower())

    def test_model_reroute_does_not_price_mixed_usage_at_luna_rates(self):
        completed, _ = self.run_plugin(reroute=True)
        self.assertEqual(completed.returncode, 0, completed.stderr)
        self.assertNotIn("cost_estimate", json.loads(completed.stdout))
        self.assertIn("Model rerouted", completed.stderr)
        self.assertIn("Cost unavailable", completed.stderr)

    def test_malformed_positional_rows_are_rejected(self):
        nullable_positions = {"events": {1}, "references": {2, 3}}
        for category in ("facts", "events", "references", "signals"):
            row = DOCUMENT_DATA[category][0]
            invalid_rows = [row[:-1], row + ["extra"], {"value": "legacy object"}]
            for position in range(len(row)):
                invalid_row = row.copy()
                invalid_row[position] = 123
                invalid_rows.append(invalid_row)
                if position not in nullable_positions.get(category, set()):
                    invalid_row = row.copy()
                    invalid_row[position] = None
                    invalid_rows.append(invalid_row)
            for invalid_row in invalid_rows:
                with self.subTest(category=category, row=invalid_row):
                    data = {**DOCUMENT_DATA, category: [invalid_row]}
                    with self.assertRaises(ValueError):
                        plugin.validate_document_data(data)

    def test_invalid_json_keeps_model_output_in_failure_log(self):
        completed, _ = self.run_plugin(json_output="invalid JSON from fixture")
        self.assertNotEqual(completed.returncode, 0)
        self.assertEqual(completed.stdout, "")
        self.assertIn("invalid JSON from fixture", completed.stderr)
        self.assertIn("Extraction failed", completed.stderr)
        self.assertIn("App-server stopped", completed.stderr)

    def test_missing_usage_is_explicit_and_not_fabricated(self):
        completed, _ = self.run_plugin(usage=False)
        self.assertEqual(completed.returncode, 0, completed.stderr)
        result = json.loads(completed.stdout)
        self.assertNotIn("statistics", result)
        self.assertNotIn("cost_estimate", result)
        self.assertIn("Turn 1: Completed", completed.stderr)
        self.assertIn("Turn 2: Completed", completed.stderr)
        self.assertIn("no token usage updates received", completed.stderr)

    def test_request_validation_failure_is_logged_before_server_start(self):
        completed, _ = self.run_plugin(protocol_version=1)
        self.assertNotEqual(completed.returncode, 0)
        self.assertEqual(completed.stdout, "")
        self.assertIn("Request received", completed.stderr)
        self.assertIn("Extraction failed", completed.stderr)
        self.assertIn("unsupported protocol version", completed.stderr)
        self.assertNotIn("App-server ready", completed.stderr)


if __name__ == "__main__":
    unittest.main()
