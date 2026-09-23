# Workspace statistics extension

Build the worker, then pass its manifest explicitly to the `pk` extension host:

```sh
go build -o internal/extensions/examples/workspace_stats/workspace-stats ./internal/extensions/examples/workspace_stats
pk run -p "Summarize this workspace" --extension internal/extensions/examples/workspace_stats/manifest.json
```

The `workspace_stats` tool and `stats` command count regular files, child directories, and bytes. Symlinks are not followed. This example is a trusted executable with the operating-system permissions of the user running `pk`; its `workspace.read` declaration is descriptive, not a sandbox.
