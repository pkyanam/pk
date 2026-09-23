# Self-development and release handoff audit

The development path is explicit: `pk update --source PATH` copies the chosen
checkout to a temporary build tree, runs `go test ./...`, builds and validates
the Go/UI pair there, and stages an immutable release. Source hashing excludes
generated `bin`, `ui/dist`, `node_modules`, and `.git`; source symlinks that
escape the checkout are rejected. The build tree is removed after staging, so
the selected checkout is not the install destination.

Activation switches the managed `current` symlink atomically. A failed health
check restores the old pointer; `/rollback` activates the recorded previous
release. The stable launcher resolves `current` once and exports an absolute
release-specific UI path. An already-running process keeps its executable and
UI path; it is not terminated by pointer replacement. The TUI's `/update` and
`/rollback` routes additionally require an idle session. `/reload` writes a
private, one-use handoff containing workspace, session, provider, model, and
effort, then launches through the stable entry point.

Provider, MCP, skill, plugin, and session data use `PK_HOME` (normally
`~/.pk`), separately from `PK_LIB_DIR`'s immutable releases. The update build
environment strips installed-launcher/session overrides; activation replaces
only the release pointer and launcher. Configuration files are not migrated
or rewritten by staging.

Fixture coverage run for this audit:

```sh
go test ./internal/update ./cmd/pk -run 'TestStageBuildsPairedReleaseFromReadOnlySourceCopy|TestActivateRollsBackOnHealthFailureAndManualRollbackRestoresPrevious|TestLauncherPinsUIToExecutingReleaseDuringActivation|TestRPCUpdateRequiresIdleAndStreamsOperationLifecycle|TestRPCUpdateCancelAndBusyGuard|TestRPCReloadRequiresIdleAndUsesValidatedHandoff|TestRPCProviderCatalogSelectionAndRuntime|TestRPCMCPToolRuntimeAndSavedSchemaMismatch|TestRPCPluginSessionUsesFrozenManifestAndRejectsSchemaDrift' -count=1
sh scripts/test-install-prebuilt.sh
```

Both passed on 2026-09-23. The staged-source test uses a fake build runner, and
the installer fixture uses a local archive/checksum plus a stub executable;
these validate control flow, pairing, rollback, and preservation boundaries,
not a complete source build or an actual installed-release replacement. No
user release or real provider configuration was changed in this audit.
