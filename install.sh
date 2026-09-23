#!/bin/sh
set -eu

REPOSITORY=${PK_INSTALL_REPOSITORY:-https://github.com/pkyanam/pk.git}
REF=${PK_INSTALL_REF:-main}

fail() {
	printf 'pk installer: %s\n' "$*" >&2
	exit 1
}

need_command() {
	command -v "$1" >/dev/null 2>&1 || fail "$1 is required. Install it with your preferred package manager, then rerun this installer."
}

need_command git
need_command go
need_command bun

GO_VERSION=$(go version 2>&1) || fail "could not run go version: $GO_VERSION"
version_at_least() {
	printf '%s\n' "$1" | awk -v required_minor="$2" '
  {
    for (i = 1; i <= NF; i++) {
      if ($i ~ /^go[0-9]+\.[0-9]+/) {
        version = $i
        sub(/^go/, "", version)
        split(version, parts, ".")
        major = parts[1] + 0
        minor = parts[2] + 0
        exit !(major > 1 || (major == 1 && minor >= required_minor))
      }
    }
    exit 1
  }'
}

# Go 1.21 introduced toolchain selection. Let the source module select Go 1.27
# through the user's GOTOOLCHAIN setting instead of incorrectly rejecting an
# older local toolchain before the module is available.
if ! version_at_least "$GO_VERSION" 21; then
	fail "Go 1.21 or newer is required for module toolchain selection (found: $GO_VERSION). Install a supported Go release and rerun; this installer does not install system packages."
fi

BUN_VERSION=$(bun --version 2>&1) || fail "could not run bun --version: $BUN_VERSION"
GIT_VERSION=$(git --version 2>&1) || fail "could not run git --version: $GIT_VERSION"

WORK_DIR=$(mktemp -d "${TMPDIR:-/tmp}/pk-install.XXXXXX") || fail "could not create a temporary install directory"
cleanup() {
	if [ -n "${WORK_DIR:-}" ] && [ -d "$WORK_DIR" ]; then
		rm -rf "$WORK_DIR"
	fi
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

SOURCE_DIR="$WORK_DIR/source"
mkdir -p "$WORK_DIR/no-hooks" "$WORK_DIR/no-template"

printf 'Fetching pk source from %s (%s)…\n' "$REPOSITORY" "$REF"
GIT_TERMINAL_PROMPT=0 GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL="$WORK_DIR/no-global-config" \
	git -c "core.hooksPath=$WORK_DIR/no-hooks" -c "init.templateDir=$WORK_DIR/no-template" \
	clone --quiet --depth 1 --single-branch --no-tags --no-recurse-submodules \
	--branch "$REF" -- "$REPOSITORY" "$SOURCE_DIR" || fail "could not fetch source. Check the repository/ref and network access, then retry."

COMMIT=$(git -C "$SOURCE_DIR" rev-parse HEAD 2>/dev/null) || fail "could not resolve the fetched source commit"
MODULE_GO_VERSION=$(cd "$SOURCE_DIR" && go list -m -f '{{.GoVersion}}' 2>&1) || fail "could not select the Go version required by source commit $COMMIT (local Go: $GO_VERSION). Install Go 1.27+ or permit Go's automatic toolchain selection; this installer does not install system packages."
EFFECTIVE_GO_VERSION=$(cd "$SOURCE_DIR" && go env GOVERSION 2>&1) || fail "could not determine the selected Go toolchain"
if ! version_at_least "$EFFECTIVE_GO_VERSION" 27; then
	fail "source commit $COMMIT requires Go $MODULE_GO_VERSION but the selected toolchain is $EFFECTIVE_GO_VERSION. Install Go 1.27+ or permit Go's automatic toolchain selection."
fi
printf 'Building pinned source commit %s with %s (module requires Go %s) and Bun %s.\n' "$COMMIT" "$EFFECTIVE_GO_VERSION" "$MODULE_GO_VERSION" "$BUN_VERSION"

# scripts/install builds in the temporary checkout and stages a paired release.
# It preserves an existing stable launcher/release for rollback and honors the
# caller's PK_BIN_DIR and PK_LIB_DIR overrides.
(cd "$SOURCE_DIR" && ./scripts/install) || fail "build or installation failed; the existing managed release remains available"

USER_HOME=$(cd ~ && pwd)
BIN_DIR=${PK_BIN_DIR:-"$USER_HOME/.local/bin"}
case ":${PATH:-}:" in
	*":$BIN_DIR:"*) ;;
	*) printf 'Add pk to PATH with: export PATH="%s:$PATH"\n' "$BIN_DIR" ;;
esac
printf 'Installed pk from commit %s. Run `pk login`, then `pk` in a project directory.\n' "$COMMIT"
