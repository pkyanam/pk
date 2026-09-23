#!/usr/bin/env python3
"""Insert deterministic low-signal test-log history before the pending prompt."""

import json
from pathlib import Path

path = Path(__file__).with_name("transcript.jsonl")
records = [json.loads(line) for line in path.read_text().splitlines() if line.strip()]
if records[-1]["seq"] == 36:
    raise SystemExit("fixture already expanded")
if records[-1]["seq"] != 35 or not records[-1]["content"].startswith("Before you answer:"):
    raise SystemExit("unexpected base fixture shape; refusing to rewrite")

packages = [
    "internal/codec", "internal/clock", "internal/retry", "internal/queue",
    "internal/metrics", "internal/config", "internal/validation", "internal/cache",
]
rows = []
for index in range(640):
    package = packages[index % len(packages)]
    shard = index % 16
    status = "cached" if index % 3 else "ok"
    rows.append(
        f"fixture-only synthetic CI detail row={index + 1:04d} "
        f"shard={shard:02d} package={package} result={status} elapsed=0.001s "
        "note=no additional receiver evidence"
    )

bulk = {
    "seq": 35,
    "kind": "tool_result",
    "name": "Bash",
    "content": (
        "Fixture-only deterministic low-signal CI replay. These repeated rows "
        "are synthetic context-volume data, not additional receiver behavior "
        "or verification evidence:\n" + "\n".join(rows)
    ),
}
pending = records[-1]
pending["seq"] = 36
records[-1] = bulk
records.append(pending)
path.write_text("".join(json.dumps(item, ensure_ascii=False, separators=(",", ":")) + "\n" for item in records))
