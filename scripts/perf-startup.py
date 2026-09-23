#!/usr/bin/env python3
"""Measure installed pk process, RPC-catalog, and OpenTUI first-frame startup."""

import argparse
import json
import math
import os
import pty
import re
import select
import statistics
import struct
import subprocess
import tempfile
import termios
import time
import fcntl
from pathlib import Path


def percentile(values, fraction):
    ordered = sorted(values)
    return ordered[max(0, math.ceil(fraction * len(ordered)) - 1)]


def stats(values):
    return {
        "n": len(values),
        "median_ms": round(statistics.median(values), 2),
        "p95_ms": round(percentile(values, 0.95), 2),
        "min_ms": round(min(values), 2),
        "max_ms": round(max(values), 2),
    }


def timed_process(pk, args, env, cwd, stdin=None, expected=None):
    started = time.perf_counter()
    result = subprocess.run(
        [str(pk), *args], input=stdin, text=True, capture_output=True,
        env=env, cwd=cwd, timeout=30,
    )
    elapsed = (time.perf_counter() - started) * 1000
    if result.returncode != 0:
        raise RuntimeError(f"pk {' '.join(args)} exited {result.returncode}: {result.stderr[-1000:]}")
    if expected and expected not in result.stdout:
        raise RuntimeError(f"pk {' '.join(args)} did not emit {expected!r}")
    return elapsed


def measure(pk, fn, samples):
    first = fn()
    warm = [fn() for _ in range(samples)]
    return {"first_process_ms": round(first, 2), "warm": stats(warm)}


def tui_first_frame(pk, env, cwd):
    pid, fd = pty.fork()
    if pid == 0:
        os.execve(str(pk), [str(pk)], env)
    fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", 24, 80, 0, 0))
    started = time.perf_counter()
    output = bytearray()
    try:
        while time.perf_counter() - started < 15:
            readable, _, _ = select.select([fd], [], [], 0.1)
            if not readable:
                continue
            try:
                output.extend(os.read(fd, 65536))
            except OSError:
                break
            plain = re.sub(rb"\x1b\[[0-?]*[ -/]*[@-~]", b"", output)
            if b"Ready" in plain and b"Ask pk to" in plain and b"Enter send" in plain:
                return (time.perf_counter() - started) * 1000
        excerpt = bytes(output[-600:]).decode("utf-8", errors="replace")
        raise RuntimeError(f"OpenTUI did not render its ready frame; output tail: {excerpt!r}")
    finally:
        try:
            os.write(fd, b"\x03")
        except OSError:
            pass
        try:
            os.close(fd)
        except OSError:
            pass
        deadline = time.time() + 3
        while time.time() < deadline:
            try:
                got, _ = os.waitpid(pid, os.WNOHANG)
            except ChildProcessError:
                break
            if got:
                break
            time.sleep(0.02)
        else:
            os.kill(pid, 9)
            os.waitpid(pid, 0)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("pk", help="installed pk launcher or binary to measure")
    parser.add_argument("--samples", type=int, default=29, help="warm subprocess samples per command (default: 29)")
    parser.add_argument("--tui-samples", type=int, default=15, help="TUI PTY launches (default: 15)")
    args = parser.parse_args()
    if args.samples < 1 or args.tui_samples < 1:
        parser.error("sample counts must be positive")
    pk = Path(args.pk).expanduser().resolve()
    if not pk.is_file():
        parser.error(f"pk executable not found: {pk}")

    with tempfile.TemporaryDirectory(prefix="pk-startup-audit-") as temp:
        base = Path(temp)
        home = base / "home"
        pk_home = base / "pk-home"
        workspace = base / "workspace"
        for directory in (home, pk_home, workspace):
            directory.mkdir(mode=0o700)
        env = os.environ.copy()
        env.update({"HOME": str(home), "PK_HOME": str(pk_home), "TERM": "xterm-256color"})

        rpc_base = [
            {"version": 1, "id": "start", "type": "start", "payload": {"workspace": str(workspace)}},
        ]

        def rpc(include_tools):
            commands = list(rpc_base)
            expected = '"type":"ready"'
            if include_tools:
                commands.append({"version": 1, "id": "tools", "type": "tools"})
                expected = '"type":"tool_catalog"'
            commands.append({"version": 1, "id": "shutdown", "type": "shutdown"})
            payload = "\n".join(json.dumps(command) for command in commands) + "\n"
            return timed_process(pk, ["rpc"], env, base, payload, expected)

        version = subprocess.run([str(pk), "version"], text=True, capture_output=True, env=env, cwd=base, timeout=30)
        if version.returncode != 0:
            raise RuntimeError(f"pk version failed: {version.stderr[-1000:]}")

        results = {
            "executable": str(pk),
            "version": version.stdout.splitlines()[0] if version.stdout else "unknown",
            "os": os.uname().sysname,
            "architecture": os.uname().machine,
            "samples_per_command": args.samples,
            "tui_samples": args.tui_samples,
            "help": measure(pk, lambda: timed_process(pk, ["--help"], env, base, expected="Usage:"), args.samples),
            "version_command": measure(pk, lambda: timed_process(pk, ["version"], env, base, expected="pk "), args.samples),
            "rpc_ready": measure(pk, lambda: rpc(False), args.samples),
            "rpc_tool_catalog_preview": measure(pk, lambda: rpc(True), args.samples),
        }
        tui_first = tui_first_frame(pk, env, base)
        tui_warm = [tui_first_frame(pk, env, base) for _ in range(args.tui_samples - 1)]
        results["tui_first_ready_frame"] = {
            "first_process_ms": round(tui_first, 2),
            "warm": stats(tui_warm) if tui_warm else None,
        }
        print(json.dumps(results, indent=2))


if __name__ == "__main__":
    main()
