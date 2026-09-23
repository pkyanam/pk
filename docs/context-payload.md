# Full provider payload example

These files are complete raw JSON request bodies captured from pk's production
runner and native Codex-compatible Responses transport. The capture used the
clean source revision [`28f1429`](https://github.com/pkyanam/pk/commit/28f14298c9a59ef433fbe3128fbba0bab3290e06), a loopback fake HTTP server, synthetic
model/account/token values, a temporary `PK_HOME`, and a disposable workspace.
The fake server returned deterministic SSE responses. No real account, API
credential, user workspace, model call, or provider response was used. Request
headers were not saved; the JSON contains no authorization header or token.

The enabled fixture extension is the repository's `workspace_stats` worker.
Its manifest declares one model tool and one slash command, and the compiled
worker actually counted two synthetic files. The prompt asks for that count;
the fake model first calls the extension, then returns a final answer, then
answers a second user turn. This gives three complete requests rather than a
handwritten approximation:

| Capture | Bytes | Conversation input in that request |
|---|---:|---|
| [Turn 1 request](examples/provider-payload/native-codex-turn-1.json) | 7,403 | Full system message, current user prompt, and nine declarations (eight core agent tools plus `workspace_stats`). |
| [Tool follow-up request](examples/provider-payload/native-codex-turn-2.json) | 7,640 | The same system/tool context, first user prompt, model `function_call`, and actual extension `function_call_output` (`2 files, 0 directories, 43 bytes`). |
| [Next user turn](examples/provider-payload/native-codex-turn-3.json) | 7,978 | The same context, first prompt, function call/result, prior assistant answer, and second user prompt. |

The tool declarations include `Bash`, `ViewImage`, `AskUser`, the five
`Subagent*` management tools, and the explicitly enabled `workspace_stats`
extension. This fixture had no user-installed skills, MCP servers, web-search
credentials, or image-generation driver; those declarations and context are
conditional on the session's actual configuration. Nothing in the example
implies that every pk session has this exact tool list.

The raw body contains the complete system text in `input[0].content`, current
and prior user/assistant items, tool schemas, model and reasoning settings,
`prompt_cache_key`, and the full tool argument/result items. Its system text is
2,969 characters in each request. These are UTF-8 byte and character counts of
this one capture only; they are not token counts. The fake provider supplied
synthetic usage values solely to exercise the response parser, so they are not
reported as measured usage.

The current default prompt separately adds the sentence “Use only tools and
integrations relevant to the request; availability alone is not a reason to
invoke them.” An isolated before/after comparison measured that addition at
108 UTF-8 bytes; it is not in these pinned-baseline payloads, and it changes no
tool schemas. The 108-byte figure is not a token count.

This uses pk's **native Codex request path** (`modelstream.NewClient` and the
pinned Responses serializer), with the endpoint redirected to loopback. It is
not a Chat Completions payload and not a packet capture from the hosted Codex
service. The fake server's response events are minimal deterministic fixtures;
only the requests are saved here. The capture harness started the RPC runner
with the same adapter and core registry used by the TUI, then enabled the
sample extension manifest. It did not start the terminal renderer itself.

To inspect the files, open the JSON directly or run:

```sh
python3 - <<'PY'
import json
from pathlib import Path

for path in sorted(Path("docs/examples/provider-payload").glob("native-codex-turn-*.json")):
    request = json.loads(path.read_text())
    print(path.name, len(path.read_bytes()), "bytes", len(request["tools"]), "tools")
    for item in request["input"]:
        if item.get("type") in {"function_call", "function_call_output"}:
            print(" ", item)
PY
```

The capture can be regenerated with `scripts/capture-provider-payload`. It
checks out the pinned source into a temporary detached worktree, copies in an
opt-in test harness, runs only the local fake-provider fixture, and writes the
three request bodies here. The normal Go test suite skips that harness unless
the script explicitly enables it. The script makes no network requests beyond
local loopback and does not use credentials.
