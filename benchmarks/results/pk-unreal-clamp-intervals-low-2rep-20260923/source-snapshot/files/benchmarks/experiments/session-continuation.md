# Follow-up continuation/cache cohort design

This is the next cohort after the Bash-default ablation; it is not part of the
output-cap pilot. Its purpose is to test realistic multi-file work and session
continuation behavior, not to rank pk against Codex. No live calls are part of
this design step.

Use one moderate self-contained Go repository with a small CLI feature that
touches parsing, core logic, and tests across at least four production files.
Keep the unrelated file count realistic (roughly 20–30 files), initialize a
Git baseline commit for every arm, and validate against pristine holdout tests.
The task should have two user-visible milestones: implement the feature and
verify it; then continue in the same task with a natural follow-up requirement
that extends the feature and again verify it. Prompts state behavior and
constraints but do not enumerate expected source edits. The fixture’s tests
remain visible for normal development, but the evaluator always restores
immutable holdout tests.

Pair two conditions on identical initial fixture and model settings:

- **Resume:** implementation runs in a new session; the follow-up resumes the
  same session ID and workspace.
- **Fresh follow-up:** implementation uses the same first prompt; the
  follow-up runs in a fresh session against the identical post-implementation
  workspace with the same follow-up prompt.

Use one repetition for the pilot, then a second with arm and task order
reversed only if setup and correctness are clean. Limit each phase to 120
seconds, the pilot to 20 minutes, and the two-repetition cohort to 40 minutes.
Checkpoint results after every phase and preserve output artifacts using the
same private/sanitized policy as the previous experiment.

Record provider-reported input, cached input, cache-write, output, and
availability for every response; do not infer a hit from stable IDs. Compare
holdout correctness, model response count, wall time, and total/uncached input
across the paired conditions. Keep the resumed and fresh-follow-up rows
separate. The conditions intentionally differ in available conversation
history, so any savings or quality change describe the product behavior of
resume versus reconstructing context from the workspace; they do not isolate
cache-key routing from prompt-history effects. A short control task may verify
that the two run paths use equal model, effort, tools, and workspace setup,
but don't multiply task variants without a clear independent question.

Report cache read/write totals even if they are zero or unavailable. A cache
benefit claim requires repeated provider-reported hits and lower uncached
input without lower holdout success or more repair responses. A cached-token
increase alone is not an efficiency result.
