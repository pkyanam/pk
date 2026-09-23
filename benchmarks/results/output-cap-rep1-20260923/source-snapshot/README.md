# Exact source snapshot for the output-cap rep1 pilot

This directory contains UTF-8 source/configuration/documentation files listed in the sibling `source-manifest.json`, copied from the isolated checkout used for the run. `files/` mirrors each repository-relative path. Every copied file was checked against that manifest SHA-256. No binaries, auth material, session directories, or private failed-command logs are included.

The run used base commit `9a169447aba03f0df4951e5e72a8c8ecda3b2a2f`, with benchmark harness files and the `pkbench` hook plus its minimal call site in `cmd/pk/main.go`. The snapshot includes the actual pre-run source; later cancellation handling and documentation edits are not part of that measured source. To verify the snapshot, hash each file under `files/` and compare with `../source-manifest.json`.
