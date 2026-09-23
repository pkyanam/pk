# Managed skill sources

Use `pk skills search QUERY` to search skills.sh, `pk skills list` to inspect local installs, `pk skills add SOURCE [SKILL]` to install from a public GitHub repository, and `pk skills remove NAME` to remove a pk-managed skill. Managed files live under `$PK_HOME/skills` (normally `~/.pk/skills`) and are included in the skill directories for new sessions.

When a source has one discovered skill, `add` selects it directly. If there are several, it prints their names and paths; pass one of those as `SKILL` to choose explicitly. Skill instructions apply to new sessions; use `/new` in the TUI after a change.

Search uses `https://skills.sh/api/search`, the unauthenticated endpoint currently called by the upstream [`vercel-labs/skills` CLI](https://github.com/vercel-labs/skills/blob/main/src/find.ts). This endpoint is not the documented stable API: skills.sh documents `/api/v1/skills/search` separately and requires Vercel OIDC for it. Search is best-effort; if it is unavailable, browse a repository directly with `owner/repo` or a public GitHub/skills.sh skill URL.

Before installation, `pk` lists the repository's skills and lets you choose one. Supported sources include `owner/repo`, `owner/repo@skill`, a GitHub repository or tree URL, and a `https://skills.sh/<owner>/<repo>/<skill>` URL. Private repositories, non-GitHub hosts, and skills.sh packs are not supported in this version.

Installation copies the selected skill's instruction and supporting files into the managed directory. It does not run scripts or follow repository symlinks. The manager bounds GitHub responses, discovery time, file count, and total copied bytes; it refuses an existing skill directory rather than replacing it. Installed entries carry a local provenance marker, so remove only acts on a skill installed by pk. Search and source metadata come from remote services and should be reviewed like other third-party instructions.

Installed skills are loaded when a session starts. Start `/new` after installing or removing a skill to use the current skill catalog; saved sessions keep their captured context.
