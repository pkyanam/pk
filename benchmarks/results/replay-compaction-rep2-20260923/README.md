# Replay-compaction pilot record

This directory preserves the two-repetition paired pk-only replay-compaction
pilot. It contains the generated summary, sanitized per-phase JSONL telemetry,
paired aggregate deltas, generated Go source artifacts (`artifacts/`), and the source snapshot that was hash-verified against
`source-manifest.json`. The source base is pk revision `8e2783d`; source-tree
SHA-256 is `4dbf008eeaac5a791fbef3d5851f498f8177a675d964d3927500cee330f795d8`.
The source diff hash is the SHA-256 of the empty diff, so the recorded source
tree matches that committed revision.

Session, response, call, and operation IDs were removed from exported JSONL and
summary records. Event payloads and provider usage values remain intact. No
credentials, binaries, or failed-run logs are included. Exact fixture and
runner text files are under `source-snapshot/files/`; verify them with the
manifest before using them. See `summary.md` for phase outcomes and the
experiment methodology.
