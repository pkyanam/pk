# Unreal validation record

Run on 22 September 2026 (America/New_York), macOS arm64, Go 1.27.0. Upstream: v0.1.1 / `b7c9bf1c5c2fa4127255c07727a7c8413e23944a`. A temporary clone was used; no upstream source was imported into pk's main module and no upstream issue or PR was posted.

| Check | Result |
| --- | --- |
| `go vet ./...` | Passed |
| `go build -trimpath -o <temporary-output> ./cmd/unreal-agent-runner` | Passed |
| Runner `-h` | Passed; reports prompt/JSON input, session resume, workspace, and heartbeat options |
| `go test -race -timeout 120s ./...` in local timezone | Failed in `TestExchangeRetryHints/HTTP_date`; other package results passed |
| `TZ=UTC go test -race -count=1 -timeout 120s ./...` | Passed |
| `TZ=America/New_York go test -race -count=1 ./harness/llm/responsesapi -run '^TestExchangeRetryHints/HTTP_date$'` | Reproduced failure on unchanged source |
| Same New York test after the one-line fixture patch below | Passed |
| External-module `go test -race ./...` in `experiments/unreal-composition` | Passed; public client construction and asynchronous context-prefix tests |

## Timezone-dependent fixture

The [retry test](https://github.com/unreallabsai/unreal-agent/blob/b7c9bf1c5c2fa4127255c07727a7c8413e23944a/harness/llm/responsesapi/retry_timing_test.go#L102-L105) constructs its HTTP-date header using local `time.Now()` and `http.TimeFormat`. That format includes literal GMT, so local wall-clock time is mislabeled as GMT. In New York the generated deadline is interpreted as past, and the adapter uses randomized backoff instead of the expected capped retry delay.

The fix is to convert to UTC before formatting. The [one-line patch](patches/unreal-http-date-fixture.patch) is retained for a future fork or upstream contribution (apply to the pinned checkout with `git apply --unidiff-zero`). It only changes the test fixture; this finding does not demonstrate a production retry-parser defect. UTC was used to validate the unchanged upstream suite, not to hide the local failure.

## What the composition spike establishes

[The isolated module](../../experiments/unreal-composition/README.md) depends on the released version through Go modules, with no local `replace` directive. It demonstrates public-package consumption from pk's namespace and checks an important inherited behavior: a completed asynchronous tool result appends without rewriting the already-submitted prefix.

The tests deliberately do not send model requests, execute shell tools, or use real credentials. They do not yet construct a complete pk host or verify real-provider acceptance/cache reuse. The next integration milestone is a fake-provider coordinator run with persisted resume and tool cancellation, followed by a small live-model comparison.

These checks establish buildability and useful integration seams on this host. They are not a security audit, performance benchmark, Linux/Windows validation, or reproduction of the published evaluation results. The upstream live Ollama test is opt-in; no model-performance claim follows from the offline suite.
