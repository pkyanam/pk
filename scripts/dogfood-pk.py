#!/usr/bin/env python3
"""Run one bounded pk coding-agent regression in a disposable Go workspace."""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import selectors
import shutil
import signal
import subprocess
import tempfile
import time


SOURCE = '''package clamp

// Clamp returns value limited to the inclusive interval [minimum, maximum].
func Clamp(value, minimum, maximum int) int {
	return value // DOGFOOD_BUG
}
'''

PROMPT = """In this Go workspace, fix Clamp so it returns value limited to the inclusive interval [minimum, maximum], assuming minimum <= maximum. Add focused table-driven tests covering below, at, inside, at the upper bound, and above the interval. Use Bash to inspect and edit the local files. Then make a separate Bash tool call whose command is exactly `go test ./...`, wait for it to complete, and fix any failures. Use only local workspace tools; do not delegate, access the network, or modify files outside this workspace. Briefly report the test result."""

_SUMMARY_OUTPUT: Path | None = None
_DISPOSABLE_ROOT: Path | None = None


def clean_environment(home: Path, pk_home: Path, codex_home: Path) -> dict[str, str]:
    env = dict(os.environ)
    for name in list(env):
        if name.startswith("PK_") or name.startswith("TINYFISH_"):
            env.pop(name, None)
    # Keep normal executable/toolchain paths, while isolating per-user state and
    # omitting unrelated provider credentials and extension configuration.
    allowed = {"PATH", "TMPDIR", "TEMP", "TMP", "LANG", "LC_ALL", "SYSTEMROOT"}
    env = {name: value for name, value in env.items() if name in allowed}
    env["HOME"] = str(home)
    env["PK_HOME"] = str(pk_home)
    env["CODEX_HOME"] = str(codex_home)
    env["GIT_CONFIG_NOSYSTEM"] = "1"
    env["GIT_CONFIG_GLOBAL"] = os.devnull
    return env


def run_checked(command: list[str], *, cwd: Path, env: dict[str, str], timeout: int) -> subprocess.CompletedProcess[str]:
    return subprocess.run(command, cwd=cwd, env=env, text=True, capture_output=True, timeout=timeout, check=False)


def run_prompt_bounded(command: list[str], *, cwd: Path, env: dict[str, str], timeout: int) -> subprocess.CompletedProcess[str]:
    process = subprocess.Popen(command, cwd=cwd, env=env, text=True, stdout=subprocess.PIPE,
                               stderr=subprocess.PIPE, start_new_session=True)
    try:
        stdout, stderr = process.communicate(timeout=timeout)
    except subprocess.TimeoutExpired:
        # pk may have started a Bash tool process. Terminate the whole run group
        # so a timeout does not leave workspace-changing commands behind.
        try:
            os.killpg(process.pid, signal.SIGTERM)
        except ProcessLookupError:
            pass
        try:
            stdout, stderr = process.communicate(timeout=2)
        except subprocess.TimeoutExpired:
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            stdout, stderr = process.communicate()
        raise RuntimeError(f"coding prompt exceeded {timeout}s; pk process group was stopped") from None
    return subprocess.CompletedProcess(command, process.returncode, stdout, stderr)


def rpc_event(pk: str, request: dict, expected_type: str, *, cwd: Path, env: dict[str, str], timeout: int) -> dict:
    process = subprocess.Popen([pk, "rpc"], cwd=cwd, env=env, stdin=subprocess.PIPE,
                               stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, start_new_session=True)
    selector = selectors.DefaultSelector()
    buffer = b""
    result = None
    try:
        assert process.stdin is not None and process.stdout is not None
        process.stdin.write((json.dumps(request) + "\n").encode("utf-8"))
        process.stdin.flush()
        selector.register(process.stdout, selectors.EVENT_READ)
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            ready = selector.select(max(0, deadline - time.monotonic()))
            if not ready:
                break
            chunk = os.read(process.stdout.fileno(), 4096)
            if not chunk:
                break
            buffer += chunk
            while b"\n" in buffer:
                line, buffer = buffer.split(b"\n", 1)
                if not line:
                    continue
                event = json.loads(line)
                if event.get("id") == request["id"] and event.get("type") in {expected_type, "error"}:
                    result = event
                    break
            if result is not None:
                break
        if result is None:
            raise RuntimeError("pk rpc did not return the requested local event")
        process.stdin.close()
        try:
            process.wait(timeout=3)
        except subprocess.TimeoutExpired:
            os.killpg(process.pid, signal.SIGTERM)
            try:
                process.wait(timeout=1)
            except subprocess.TimeoutExpired:
                os.killpg(process.pid, signal.SIGKILL)
                process.wait()
        if result.get("type") == "error":
            raise RuntimeError("pk rpc returned a local error")
        return result
    finally:
        selector.close()
        if process.poll() is None:
            try:
                os.killpg(process.pid, signal.SIGTERM)
            except ProcessLookupError:
                pass
            try:
                process.wait(timeout=1)
            except subprocess.TimeoutExpired:
                try:
                    os.killpg(process.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                process.wait()
        if process.stdin and not process.stdin.closed:
            process.stdin.close()
        if process.stdout:
            process.stdout.close()


def tool_catalog(pk: str, *, cwd: Path, env: dict[str, str]) -> list[str]:
    event = rpc_event(pk, {"version": 1, "id": "dogfood-tools", "type": "tools", "payload": {}},
                      "tool_catalog", cwd=cwd, env=env, timeout=15)
    names = sorted({str(item.get("name", "")) for item in event.get("payload", {}).get("tools", []) if item.get("name")})
    if "Bash" not in names:
        raise RuntimeError("tool catalog omitted Bash")
    return names


def session_usage(pk: str, session_id: str, *, cwd: Path, env: dict[str, str]) -> dict:
    # The durable usage endpoint tracks per-counter coverage and distinguishes a
    # reported zero from a missing provider field; JSONL's broad usage flag does not.
    try:
        event = rpc_event(pk, {"version": 1, "id": "dogfood-usage", "type": "session_usage",
                               "payload": {"session_id": session_id}},
                          "session_usage", cwd=cwd, env=env, timeout=30)
    except RuntimeError:
        return {"available": False, "reason": "local_usage_query_unavailable"}
    if not isinstance(event.get("payload"), dict):
        return {"available": False, "reason": "local_usage_query_unavailable"}
    payload = event["payload"]
    return {
        "available": True,
        "responses": payload.get("response_count"),
        "input_tokens": payload.get("input_tokens"),
        "output_tokens": payload.get("output_tokens"),
        "cached_input_tokens": payload.get("cached_input_tokens"),
        "uncached_input_tokens": payload.get("uncached_input_tokens"),
        "coverage": payload.get("coverage"),
    }


def save_summary(path: Path, summary: dict, temporary_root: Path | None) -> None:
    path = path.expanduser().resolve()
    if temporary_root is not None and (path == temporary_root or temporary_root in path.parents):
        raise RuntimeError("summary output must be outside the disposable workspace")
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary_path = None
    try:
        with tempfile.NamedTemporaryFile(mode="w", encoding="utf-8", dir=path.parent,
                                         prefix=".pk-dogfood-", delete=False) as output:
            temporary_path = Path(output.name)
            os.chmod(temporary_path, 0o600)
            json.dump(summary, output, sort_keys=True)
            output.write("\n")
        os.replace(temporary_path, path)
    finally:
        if temporary_path is not None and temporary_path.exists():
            temporary_path.unlink()


def main() -> int:
    global _SUMMARY_OUTPUT, _DISPOSABLE_ROOT
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--pk", default=shutil.which("pk"), help="pk executable (default: pk on PATH)")
    parser.add_argument("--timeout", type=int, default=240, help="maximum live prompt time in seconds")
    parser.add_argument("--codex-auth-file", help="existing Codex auth file to read (default: CODEX_HOME/auth.json or ~/.codex/auth.json)")
    parser.add_argument("--output", type=Path, help="optional JSON summary path outside the temporary workspace")
    args = parser.parse_args()
    _SUMMARY_OUTPUT = args.output
    if not args.pk:
        raise RuntimeError("pk executable was not found")
    pk = str(Path(args.pk).expanduser().resolve())
    codex_home = Path(os.environ.get("CODEX_HOME", Path.home() / ".codex")).expanduser().resolve()
    auth_file = Path(args.codex_auth_file).expanduser().resolve() if args.codex_auth_file else codex_home / "auth.json"
    if not auth_file.is_file():
        raise RuntimeError("existing Codex auth file is required; pass --codex-auth-file")
    go = shutil.which("go")
    if not go:
        raise RuntimeError("Go is required for the workspace checks")
    if args.timeout <= 0:
        raise RuntimeError("timeout must be positive")

    started = time.monotonic()
    with tempfile.TemporaryDirectory(prefix="pk-dogfood-") as temporary:
        root = Path(temporary)
        _DISPOSABLE_ROOT = root
        home, pk_home, workspace, skills = (root / name for name in ("home", "pk-home", "workspace", "skills"))
        for directory in (home, pk_home, workspace, skills):
            directory.mkdir(mode=0o700)
        (workspace / "go.mod").write_text("module dogfood.local/clamp\n\ngo 1.23\n", encoding="utf-8")
        (workspace / "clamp.go").write_text(SOURCE, encoding="utf-8")
        env = clean_environment(home, pk_home, codex_home)
        env["PK_HOME"] = str(pk_home)

        version = run_checked([pk, "version"], cwd=workspace, env=env, timeout=10)
        if version.returncode != 0:
            raise RuntimeError("could not identify pk executable")
        names = tool_catalog(pk, cwd=workspace, env=env)
        if not ("Bash" in names and "ViewImage" in names):
            raise RuntimeError("tool catalog did not include expected core local tools")

        command = [pk, "-p", PROMPT, "--workspace", str(workspace), "--skills-dir", str(skills),
                   "--model", "gpt-6-luna", "--effort", "low", "--use-codex",
                   "--codex-auth-file", str(auth_file), "--jsonl"]
        prompt_started = time.monotonic()
        result = run_prompt_bounded(command, cwd=workspace, env=env, timeout=args.timeout)
        prompt_wall_ms = round((time.monotonic() - prompt_started) * 1000)
        events = []
        for line in result.stdout.splitlines():
            try:
                value = json.loads(line)
                if isinstance(value, dict):
                    events.append(value)
            except json.JSONDecodeError:
                continue
        bash_events = [event for event in events if event.get("type") == "tool_call" and str(event.get("name", "")).casefold() == "bash"]
        calls_by_id: dict[str, dict[str, bool]] = {}
        for event in bash_events:
            call_id = str(event.get("call_id", ""))
            command_text = str(event.get("command_preview", "")) + " " + str(event.get("arguments_preview", ""))
            record = calls_by_id.setdefault(call_id, {"command_has_go_test": False, "completed": False})
            record["command_has_go_test"] = record["command_has_go_test"] or "go test ./..." in command_text
            record["completed"] = record["completed"] or event.get("state") == "completed"
        test_calls = [call for call in calls_by_id.values() if call["command_has_go_test"]]
        if result.returncode != 0:
            raise RuntimeError(f"coding prompt exited {result.returncode}; see local pk diagnostics and retry only after reviewing them")
        if not test_calls or not any(call["completed"] for call in test_calls):
            summary = {
                "result": "failed", "error": "completed go test tool call not observed",
                "pk_version": version.stdout.strip().splitlines()[0] if version.stdout.strip() else "unknown",
                "tool_catalog": names, "observed_tool_events": len(events),
                "bash_tool_calls": len(calls_by_id), "bash_tool_events": len(bash_events),
                "bash_states": sorted({str(event.get("state", "unknown")) for event in bash_events}),
                "bash_command_preview_present": any(bool(event.get("command_preview")) for event in bash_events),
                "prompt_wall_ms": prompt_wall_ms,
            }
            if args.output:
                save_summary(args.output, summary, root)
                summary["summary_file"] = str(args.output.expanduser().resolve())
            print(json.dumps(summary, sort_keys=True))
            return 1
        session_ids = [str(event.get("session_id", "")) for event in events if event.get("session_id")]
        if not session_ids:
            raise RuntimeError("pk JSONL did not expose the run's session for usage lookup")

        # This holdout is unavailable to the model; add it only after its run.
        holdout = root / "holdout_test.go"
        holdout.write_text('''package clamp

import "testing"

func TestDogfoodHoldoutBoundaries(t *testing.T) {
	cases := []struct{ value, want int }{{-5, 0}, {0, 0}, {5, 5}, {10, 10}, {15, 10}}
	for _, tc := range cases {
		if got := Clamp(tc.value, 0, 10); got != tc.want {
			t.Errorf("Clamp(%d, 0, 10) = %d, want %d", tc.value, got, tc.want)
		}
	}
}
''', encoding="utf-8")
        shutil.copyfile(holdout, workspace / "dogfood_holdout_test.go")
        verified = run_checked([go, "test", "./..."], cwd=workspace, env=env, timeout=60)
        if verified.returncode != 0:
            raise RuntimeError("post-run immutable holdout failed")
        usage = session_usage(pk, session_ids[-1], cwd=workspace, env=env)

        summary = {
            "result": "passed",
            "pk_version": version.stdout.strip().splitlines()[0] if version.stdout.strip() else "unknown",
            "model": "gpt-6-luna",
            "effort": "low",
            "tool_catalog": names,
            "bash_tool_calls": len(calls_by_id),
            "bash_tool_events": len(bash_events),
            "completed_go_test_tool_calls": len([call for call in test_calls if call["completed"]]),
            "immutable_holdout": "passed",
            "usage": usage,
            "prompt_wall_ms": prompt_wall_ms,
            "total_wall_ms": round((time.monotonic() - started) * 1000),
            "workspace": "temporary; removed on exit",
            "auth": "existing Codex auth file read only; never copied or printed",
        }
        if args.output:
            save_summary(args.output, summary, root)
            summary["summary_file"] = str(args.output.expanduser().resolve())
        print(json.dumps(summary, sort_keys=True))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except KeyboardInterrupt as error:
        failure = {"result": "failed", "error": "interrupted", "error_type": type(error).__name__}
        if _SUMMARY_OUTPUT:
            try:
                save_summary(_SUMMARY_OUTPUT, failure, _DISPOSABLE_ROOT)
                failure["summary_file"] = str(_SUMMARY_OUTPUT.expanduser().resolve())
            except OSError:
                pass
        print(json.dumps(failure, sort_keys=True))
        raise SystemExit(130)
    except (OSError, RuntimeError, subprocess.SubprocessError) as error:
        # Keep output safe: subprocess errors may contain paths or arguments.
        message = str(error).casefold()
        category = "timeout" if "exceed" in message or "timeout" in message else "check_failed"
        failure = {"result": "failed", "error": category, "error_type": type(error).__name__}
        if _SUMMARY_OUTPUT:
            try:
                save_summary(_SUMMARY_OUTPUT, failure, _DISPOSABLE_ROOT)
                failure["summary_file"] = str(_SUMMARY_OUTPUT.expanduser().resolve())
            except OSError:
                pass
        print(json.dumps(failure, sort_keys=True))
        raise SystemExit(1)
