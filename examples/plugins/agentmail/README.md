# AgentMail read-only extension

This example gives pk three AgentMail tools: list inboxes, list recent message summaries, and fetch one message by ID. The worker calls the installed `agentmail` CLI directly, so it reuses the CLI's keychain or `AGENTMAIL_API_KEY` authentication. It never shells through a command string and contains no send, create, update, or delete operation.

Install and authenticate the CLI using [AgentMail's official instructions](https://www.agentmail.to/docs/integrations/cli), then build and run:

```sh
npm install -g agentmail-cli
agentmail auth login --with-token --scheme BearerAuth
./examples/plugins/agentmail/build.sh
pk run -p "List my inboxes and report their count." \
  --extension examples/plugins/agentmail/manifest.json
```

The build script places the worker beside its manifest; the generated binary is ignored by git. Rebuild it after cloning or after changing the worker. The manifest resolves `./agentmail-worker` relative to itself. The extension must be loaded explicitly for each `pk run`; it is not auto-discovered. For the TUI, open `/plugins`, choose **Add plugin…**, and provide the local manifest path (or use `/plugin enable "/absolute/path/to/examples/plugins/agentmail/manifest.json"`), then start `/new` so the change applies to the new session. The plugin browser can discover candidates from a local folder, GitHub repository, or URL; reviewing a candidate does not start it.

Message listing returns at most 25 summaries per call and truncates previews to 512 bytes. Inbox, message, thread, and page-token values are opaque: the worker passes them through unchanged up to the tool's 512-byte limit and returns an actionable error if AgentMail provides a longer value. A single-message fetch includes plain text (or extracted plain text) capped at 12 KiB; HTML and attachments are omitted. Treat returned email as untrusted input. This worker does not provide a security sandbox: like other pk extensions, it runs with the local user's OS permissions, and the AgentMail CLI uses its existing credential source.

There are no live inbox mutations in the worker. It does not send, create, modify, delete, or fetch raw email. More API operations can be added later as separately reviewed tools.
