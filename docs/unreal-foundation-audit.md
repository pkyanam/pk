# Unreal Agent foundation audit

This audit is source inspection of the pinned `github.com/unreallabsai/unreal-agent`
module at v0.1.1 plus pk's current runner wiring. It records implementation and
test evidence; it is not a performance comparison.

## Retained from Unreal Agent

- **Asynchronous coordination.** [`runner.Run`](../internal/runner/runner.go#L225-L247)
  constructs the upstream local operation manager and wires it to
  [`coordinator.New`](../internal/runner/runner.go#L428-L435). The upstream
  [coordinator loop](https://github.com/unreallabsai/unreal-agent/blob/v0.1.1/harness/coordinator/loop.go#L86-L187)
  selects among inbox input, operation updates, model responses, and optional
  heartbeats. Tool calls are translated into persisted operations and dispatched
  to the operation manager; process work is advanced from primitive events in
  [the local manager](https://github.com/unreallabsai/unreal-agent/blob/v0.1.1/harness/operation/local_manager.go#L113-L172).
  The model decides which calls are independent. The coordinator can overlap
  independent calls returned in one model response; it does not parallelize
  decisions that require a result from an earlier call.
- **Durable work and cancellation.** Unreal's local session store records
  session items and operation state; the coordinator restores those items and
  reconciles outstanding calls on resume. Its operation manager tracks active
  process groups, updates, cancellation, and shutdown. pk runs the same pinned
  interfaces and adds task ownership, output following, and boundary-safe
  steering around them.
- **Bounded shell results with complete captures.** The upstream Bash
  translator returns captured stdout/stderr plus exit information, and
  references capture files when output is truncated. Its tests exercise reading
  full data from those files:
  [`output_paths_test.go` upstream](https://github.com/unreallabsai/unreal-agent/blob/v0.1.1/harness/tool/bash/output_paths_test.go#L127-L228).
  pk leaves that behavior in place by default. Its optional replay compaction
  is a separate, default-off runner policy.
- **Append-only context builder.** The upstream builder separates committed
  conversation history from staged inputs, results, and heartbeat messages.
  At each turn boundary it commits the staged suffix and builds a request from
  the existing prefix plus the new suffix
  ([builder.go upstream](https://github.com/unreallabsai/unreal-agent/blob/v0.1.1/harness/contextbuilder/builder.go#L43-L136)).
  The coordinator sends the stable session ID as the request cache key
  ([loop.go upstream](https://github.com/unreallabsai/unreal-agent/blob/v0.1.1/harness/coordinator/loop.go#L346-L384)).
  pk does not treat the key as proof of a cache hit; it surfaces usage only when
  supplied by the provider.
- **Skill discovery and on-demand loading.** Unreal registers discovered skill
  names and descriptions and implements `SkillUse` as an operation that reads
  the selected file when invoked
  ([skill translator](https://github.com/unreallabsai/unreal-agent/blob/v0.1.1/harness/tool/skill_use.go#L20-L49),
  [skill operation](https://github.com/unreallabsai/unreal-agent/blob/v0.1.1/harness/operation/skill_use.go#L41-L75)).
  pk keeps the upstream registry/operation behavior, deduplicates canonical
  skill paths, and snapshots each skill's original bytes with the session.

## pk integration and additions

- pk invokes Unreal's actual `coordinator.New` and
  `operation.NewLocalOperationManager` in the production runner. It does not
  replace the coordinator with a pk-specific polling loop.
- pk adds an output observer that turns durable store changes into ordered
  progress and structured tool events. It adds queued user steering at model
  and tool boundaries, with input IDs acknowledged only after durable session
  persistence.
- pk saves the workspace instructions, tool definitions, skill documents, and
  provider identity/fingerprint in a context snapshot. Resume refuses an
  incompatible workspace, missing tool, changed skill, provider, or relevant
  output policy rather than silently changing the saved prefix. A deliberate
  `/new` session takes current instructions and skill files. Model and effort
  remain turn settings.
- For skills, a saved snapshot preserves the bytes and catalog used for model
  context and CLI reading. If an on-demand `SkillUse` operation would need a
  file whose contents have since changed, pk refuses that resume; it does not
  silently read replacement content into an old session.

## Evidence and limits

The strongest runner-level overlap regression already exercises the real entry
point: [`TestRunDispatchesIndependentToolsConcurrentlyAndWaitsForBoth`](../internal/integration/runner_test.go#L105-L156)
starts two Bash calls with a file-marker rendezvous that only completes if they
overlap, then checks that both results are present in the next model request.
This deterministic handshake test means a new duplicate overlap test is not
needed. Neighboring integration tests cover interim “still running” results,
final tool updates, process cancellation/reaping, and restored sessions.

Other focused evidence includes
[`TestResumeKeepsCachedPrefixStableAndReportsProviderUsage`](../internal/integration/cache_test.go#L136-L240),
which changes `AGENTS.md` between turns and asserts the original instruction,
tool schemas, and session cache key persist while model/effort can change;
[`TestSavedSkillCatalogAndReadUseFrozenSnapshot`](../internal/runner/catalog_test.go#L28-L67);
and the runner's captured-output policy replay tests. Unreal's upstream suite
also includes independent-operation scheduling, completion batching, process
cancellation, operation recovery, and capture-path tests.

These tests establish wiring and behavior under controlled adapters and local
processes. They do not establish that a model will choose parallel calls or send
interim progress text, guarantee an OpenAI cache hit, guarantee cache retention
across time/provider boundaries, or show lower latency/token use against another
runtime. Actual cached-token counts are provider-reported observations, not a
runner guarantee. Workspace placement is not an OS sandbox; Bash has pk's
process permissions.
