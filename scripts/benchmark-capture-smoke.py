#!/usr/bin/env python3
"""Exercise tagged CLI capture against loopback SSE, without real credentials."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


def main():
    repo = Path(__file__).resolve().parents[1]
    requests = []

    class Provider(BaseHTTPRequestHandler):
        def log_message(self, *_):
            pass

        def do_POST(self):
            assert self.path == "/v1/chat/completions"
            requests.append(json.loads(self.rfile.read(int(self.headers["Content-Length"]))))
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.end_headers()
            event = {"id": "capture-fixture", "choices": [{"index": 0, "delta": {"content": "CAPTURE_FINAL_SENTINEL"}, "finish_reason": "stop"}], "usage": {"prompt_tokens": 10, "completion_tokens": 2, "prompt_tokens_details": {"cached_tokens": 0}}}
            self.wfile.write(("data: " + json.dumps(event) + "\n\ndata: [DONE]\n\n").encode())

    with tempfile.TemporaryDirectory(prefix="pk-capture-smoke-") as directory:
        root = Path(directory)
        for name in ("home", "workspace", "skills"):
            (root / name).mkdir()
        binary = root / "pk"
        subprocess.run(["go", "build", "-tags", "pkbench", "-o", str(binary), "./cmd/pk"], cwd=repo, check=True)
        env = {k: v for k, v in os.environ.items() if not k.startswith("PK_")}
        env["PK_HOME"] = str(root / "home")
        server = ThreadingHTTPServer(("127.0.0.1", 0), Provider)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            def run(*args):
                return subprocess.run([str(binary), *args], env=env, cwd=root / "workspace", capture_output=True, text=True, timeout=30, check=True)
            run("provider", "add", "--id", "fixture", "--protocol", "chat_completions", "--base-url", f"http://127.0.0.1:{server.server_port}/v1", "--model", "fixture")
            env["PK_BENCH_POLICY"] = "subagent-schema-current"
            metrics = root / "metrics.jsonl"
            env["PK_BENCH_CONTEXT_METRICS_FILE"] = str(metrics)
            result = run("run", "--provider", "fixture", "--skills-dir", str(root / "skills"), "--jsonl", "--prompt", "CAPTURE_PRIVATE_SENTINEL")
            assert len(requests) == 1
            assert "CAPTURE_FINAL_SENTINEL" in result.stdout
            raw = metrics.read_text()
            assert "CAPTURE_PRIVATE_SENTINEL" not in raw and "CAPTURE_FINAL_SENTINEL" not in raw
            records = [json.loads(line) for line in raw.splitlines()]
            assert len(records) == 1
            record = records[0]
            assert record["response_id"] == "capture-fixture" and not record["request_failed"]
            assert record["usage"]["cached_input_tokens_available"]
            assert record["usage"]["cached_input_tokens"] == 0
            assert metrics.stat().st_mode & 0o777 == 0o600
            assert record["tool_schemas"]["items"] == len(requests[0]["tools"])
            baseline_tools = {tool["function"]["name"] for tool in requests[0]["tools"]}
            helpers = {"SubagentStart", "SubagentStatus", "SubagentSend", "SubagentWait", "SubagentCancel"}
            assert helpers <= baseline_tools and "Subagent" not in baseline_tools
            env["PK_BENCH_POLICY"] = "compact-subagent-schema"
            compact_metrics = root / "compact-metrics.jsonl"
            env["PK_BENCH_CONTEXT_METRICS_FILE"] = str(compact_metrics)
            compact = run("run", "--provider", "fixture", "--skills-dir", str(root / "skills"), "--jsonl", "--prompt", "CAPTURE_PRIVATE_SENTINEL")
            assert len(requests) == 2
            compact_tools = {tool["function"]["name"] for tool in requests[1]["tools"]}
            assert compact_tools == (baseline_tools - helpers) | {"Subagent"}
            compact_records = [json.loads(line) for line in compact_metrics.read_text().splitlines()]
            assert len(compact_records) == 1
            compact_record = compact_records[0]
            assert compact_record["tool_schemas"]["items"] == len(requests[1]["tools"])
            assert compact_record["tool_schemas"]["bytes"] < record["tool_schemas"]["bytes"]
            assert "CAPTURE_PRIVATE_SENTINEL" not in compact_metrics.read_text()
            assert "CAPTURE_FINAL_SENTINEL" in compact.stdout
            schema_events = []
            for output in (result.stdout, compact.stdout):
                events = [json.loads(line) for line in output.splitlines()]
                measured = [event for event in events if event.get("type") == "benchmark_subagent_schema"]
                assert len(measured) == 1 and measured[0]["subagent_schema_metrics_available"]
                schema_events.append(measured[0])
            assert schema_events[0]["subagent_tool_definition_bytes_saved"] == 0
            assert schema_events[1]["subagent_tool_definition_bytes_saved"] == record["tool_schemas"]["bytes"] - compact_record["tool_schemas"]["bytes"]
            print(json.dumps({"result": "passed", "requests": 2, "baseline_schemas": record["tool_schemas"], "dispatcher_schemas": compact_record["tool_schemas"], "system_prompt": record["system_prompt"], "note": "Local synthetic usage only; no cost or quality measurement."}))
        finally:
            server.shutdown()
            server.server_close()
            thread.join()


if __name__ == "__main__":
    main()
