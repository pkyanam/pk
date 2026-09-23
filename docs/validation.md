# Validation record

This record summarizes smoke evidence gathered during the OpenTUI and detached-task upgrade on 22
September 2026. It contains no credentials, account details, or private task IDs.

## Live smoke checks

- A detached task was created with a fresh `/tmp` workspace. The launching `pk task create` command
  returned before the worker finished; the worker used the default Luna/medium settings, wrote a
  small Go module with a `Greet` function and test, and reached `succeeded` in about 14 seconds.
  `pk task status` reported the final state, `pk task attach` replayed the stored final output, and an
  independent `go test` in that workspace passed.
- From the installed TUI, `/task new --workspace /tmp/pk-tui-task-check ...` created a separate
  workspace, wrote `hello.txt`, and finished successfully with Luna/medium. The resulting file
  contained the expected `PK_TUI_TASK_OK` marker.
- A multi-tool Luna conversation returned provider usage counters for three successive responses.
  The reported input counts were 681, 3,626, and 5,326 tokens; cached-input counts were 0, 0, and
  2,560. This demonstrates one observed cache reuse. It is a short smoke sample, not a benchmark or a
  prediction that other requests will hit the cache.
- An initial 80×24 terminal review found clipped layout. After the layout fix, an interactive PTY
  smoke on an 80×24 terminal showed the composer and footer, accepted a prompt, received `PK_TUI_LUNA_OK`
  from Luna, and exited cleanly on Ctrl-C with status 0.
- A later installed PTY smoke at 120×36 showed a Bash `pwd`/`ls` call with its command, output, and
  exit result; then a Luna progress message; then a second Bash `printf` producing `PK_RICH_OK`; and
  finally the assistant response. Tool details updated in place without object-string artifacts.

## Automated checks

- The root Go package's `go test -race -count=1 -timeout 120s ./...` passed on the rich-progress
  checkpoint.
- `go test ./internal/runner ./internal/integration -count=1` passed after the progress-event changes.
  `TestMultiStepRunPreservesProgressAndStructuredToolUpdates` checks ordered commentary and final
  messages, tool updates keyed by call ID, shell excerpts/exit status, and redaction of a test API key.
  `TestToolEventsReportRunningThenCompleted` checks the operation state transition. Context snapshot
  tests cover restoring saved prompt context and refusing changed skill contents.
- The OpenTUI build, TypeScript check, and seven renderer tests passed after tool-preview rendering.
  Renderer coverage verifies completed tool rows retain their result excerpts. The transcript tail is
  capped in code; no large-scale long-loop stress test has been run.
- The GitHub Actions workflow runs Go build, vet, and race tests for pk and the public composition
  module on Linux and macOS. It retains the pinned Unreal Agent v0.1.1 race-test suite in UTC on
  Linux. A separate Linux/macOS OpenTUI job installs dependencies with `bun install
  --frozen-lockfile`, builds the frontend, typechecks it, and runs the Bun tests with Bun 1.3.14.
  The hosted workflow rerun passed on the earlier mockup-only commit; later source changes still need
  their own hosted run.
