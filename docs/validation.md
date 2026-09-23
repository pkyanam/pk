# Validation record

This record summarizes local alpha checks run on 22 September 2026 with Go 1.27.0 on macOS arm64.
It contains no account details, tokens, or private session identifiers.

## Live smoke checks

- `go install` placed pk on `~/.local/bin`, and a fresh zsh found the installed `pk` on `PATH`.
- Native device-code login completed and saved credentials to pk's private auth file.
- A low-effort Astra request with explicit `--use-codex` returned `PK_ASTRA_OK`.
- From a temporary project directory, interactive pk used Bash to create and read `hello.txt`; the
  tool returned `terminal-agent-ok`, and a separate shell check confirmed the file contents.
- A second prompt in the same interactive session recalled `cobalt-fern` from the first turn. A
  separate invocation also resumed a saved session and returned `indigo-tulip`, supplied in an earlier
  turn. Private session IDs are omitted.
- Ctrl-C during a Bash `sleep 120` stopped the run, exited with status 130, and left the child PID
  absent when pk returned.
- Bare `pk`, `/help`, `/exit`, and idle-terminal Ctrl-C behavior were also checked locally.

These checks do not establish parallel live model requests or provide performance measurements.

## Automated coverage

The local integration tests use a scripted provider adapter and temporary workspaces. They cover
concurrent independent Bash calls, the running-tool placeholder and later result, session resume,
caller cancellation, and child-process cleanup. They do not use real credentials or make model
requests. The live smoke checks above provide separate evidence for provider access and Bash output;
the live checks did not measure parallel model requests or performance.

On the root module, `go build -trimpath ./...`, `go vet ./...`, and
`go test -race -count=1 ./...` passed. The public composition module's build, vet, and race tests also
passed. The pinned Unreal Agent v0.1.1 source was tested unchanged with
`TZ=UTC GOPROXY=off go test -race -count=1 -timeout 120s ./...`; the suite passed. UTC is intentional:
the pinned upstream HTTP-date retry fixture formats local time with a literal GMT suffix, so this
fixture is timezone-dependent. See the [upstream validation notes](research/unreal-validation.md) for
the reproduction and fixture detail.

The GitHub Actions workflow defines Go 1.27 build, vet, and race-test checks for pk and the public
composition module on Linux and macOS, plus the pinned upstream suite in UTC on Linux. These are
workflow checks; the results above are local checks.
