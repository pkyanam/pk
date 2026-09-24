# Workspace journal

The workspace journal records file changes that pk's file tools make so they
can be inspected and selectively restored. It is recovery tooling with narrow,
verified coverage — not a sandbox, not a backup, and not a guarantee against
data loss.

## What is recorded

`WriteFile` and `EditFile` mutations are journaled under
`PK_HOME/journal` (default `~/.pk/journal`) in a content-addressed store:

- `objects/` holds content blobs addressed by SHA-256.
- `sessions/<session>/journal.jsonl` is the append-only entry log.
- `sessions/<session>/restores/` holds pre-restore safety snapshots.

Each mutation is recorded durably **before** the file changes: the preimage
(and the intended postimage) is stored and a `prepared` line is fsynced before
the atomic write. The terminal `completed` or `failed` line is recorded
afterward. A `prepared` entry without a terminal line means completion is
unknown — the workspace may or may not contain that change; it is never
reported as clean or clean-able. Failures where nothing changed (invalid
arguments, a missing `old_string`, a stale `expected_sha256`) are not recorded,
because nothing was mutated.

The readable fold defaults to 256 entries per session. This does **not** cap
physical disk usage: raw logs are not compacted and the configured
`MaxObjectBytes` limit is not yet enforced. Explicit object collection refuses
to delete anything if it cannot scan every session within its scan limit.
Do not run collection concurrently with writers; cross-process coordination
of the shared object store is not implemented.

## Coverage and honesty

The journal covers **only** `WriteFile` and `EditFile`. Bash writes, external
editor edits, and generated image outputs are unobserved. An empty journal
never means the workspace is clean; summaries and tool replies state this
coverage limit explicitly. Journaling is not an OS boundary: the tools still
run with pk's ordinary permissions.

## WorkspaceDelta (model tool)

New sessions with journaling enabled include a read-only `WorkspaceDelta`
tool. Its description asks the model to call it after file-tool edits, before
reporting completion; the bundled pk skill repeats that cue. Tool availability
is also shown in the initial catalog preview. This is guidance, not a guarantee
that every model will call it.


- Default: a bounded summary of recorded changes — per-path
  created/modified/failed/restored counts, session totals, and a coverage
  note.
- `cursor`: decimal journal sequence from a previous reply; the summary covers
  entries at or before the cursor. Diff requests honor the same cutoff. Omit
  the cursor for the latest state; it is not a changes-since cursor.
- `diff=true` with `path` (latest mutation for that path) or `op_id`: one
  bounded unified diff (context lines, hunk and byte caps) between that
  operation's preimage and postimage.

The tool is read-only and never mutates the workspace. `path` and `op_id`
require `diff=true`; a `path` that has no retained completed mutation is an
error, not an empty reply. If the store is unavailable, callers receive an
error rather than an unverifiable claim.

## pk journal (CLI)

```sh
pk journal list SESSION [--json]      # readable fold of recorded operations
pk journal diff SESSION --path PATH   # bounded diff of the latest mutation
pk journal diff SESSION --op-id ID    # bounded diff of one operation
pk journal plan SESSION --workspace DIR [--op ID]...   # dry run
pk journal restore SESSION --workspace DIR [--op ID]... --yes
```

Restore is user-initiated only; it is not a model tool and there is no
approval gate on `WriteFile`/`EditFile` themselves.

## Restore semantics

- Without `--op`, restore selects every completed or restored mutation that
  still has a stored preimage and was not superseded by a later recorded
  mutation of the same path.
- With `--op`, exactly the named operations.
- **Fingerprint guard**: a file is only rewritten when its current content
  matches the recorded postimage (or the op was already restored). A file the
  user edited since is skipped with a reason; user edits are never overwritten
  and any conflict detected in preflight prevents the whole selected batch.
  All preimages are verified before writes, with a 256 MiB aggregate cap.
  Late filesystem errors or concurrent external edits can still leave a
  partially applied batch; the report identifies applied and skipped paths.
- **Safety snapshot**: before any change, current content of every affected
  file is snapshotted into `restores/<timestamp>-<id>/`, so recovery remains
  possible and restore is repeatable.
- Refusals are explicit: missing file, non-regular file (symlinks are never
  followed or replaced), missing journal object, or a file that changes during
  the restore.
- Each applied change is appended to the journal, so later `list`/`diff`
  output reflects it.

## RPC and TUI

The foreground session exposes `journal` (list/summary/diff) and
`journal_restore` RPC routes. Restores run only while the foreground session
is idle, like `/compact`, and a completed restore is appended to the session
history as a user-visible note so the model knows workspace state changed. The
TUI `/journal` panel lists restorable changes with keyboard and mouse
confirmation and a bounded original-change diff before confirmation; restore
reverses that displayed change. Restore is always explicit. CLI restores also
acquire the session lease and append a report when the saved session exists.
Failures to record the note are surfaced rather than silently claiming success.

## Out of scope

- Bash/external-edit observation. A future opt-in boundary scan could add a
  bounded change *detection* layer, but it cannot provide preimages.
- Restoring files to a point in time (as opposed to reverting recorded
  operations). There is no whole-workspace snapshot; missing preimages are
  reported as unrestorable.
- Automatic or model-initiated restore, and any approval-gating of writes.
