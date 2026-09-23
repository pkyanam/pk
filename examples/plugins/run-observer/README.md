# Run lifecycle observer

This tool-free extension demonstrates opt-in observation of `run_start`,
`response_complete`, and `run_end`. It logs only event type, run/session IDs, model,
and status as JSON Lines to worker stderr. It omits the workspace path and never
reads or logs prompts, responses, tool arguments, or tool results.

Build it from the repository root:

```sh
./examples/plugins/run-observer/build.sh
```

Then load its manifest explicitly for a one-shot run:

```sh
pk run -p "Summarize this project" --extension examples/plugins/run-observer/manifest.json
```

Or discover and install it from the checkout with the plugin browser. It declares
no tools or commands, so its only behavior is observing lifecycle events. When
adding it to an existing pk session, start a new session for the manifest to take
effect.

Lifecycle notifications are best-effort and bounded; queue pressure, worker
shutdown, or a short observer timeout can drop an event. They are not a durable
audit log, do not alter model input/output, and make no performance claim. pk
currently captures worker stderr for bounded diagnostics, so log visibility
depends on the host and errors; the observer does not create or append a file.

The current development tree supports lifecycle notifications. A host must offer
`lifecycle_notifications` during initialization for the worker to opt in and
receive events. Older workers remain compatible with hosts that do not offer
that feature; older hosts that reject lifecycle hooks cannot load this manifest.
