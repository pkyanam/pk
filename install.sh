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
need_command bun

BUN_VERSION=$(bun --version 2>&1) || fail "could not run bun --version: $BUN_VERSION"

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

hash_file() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{print $1}'
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | awk '{print $1}'
	else
		return 1
	fi
}

download_status() {
	DOWNLOAD_STATUS=$(curl --proto '=https' --proto-redir '=https' --silent --show-error --location --connect-timeout 10 --max-time 300 \
		--max-filesize "$3" --output "$2" --write-out '%{http_code}' "$1") || return 2
	case "$DOWNLOAD_STATUS" in
		200) return 0 ;;
		404) return 1 ;;
		*) return 2 ;;
	esac
}

try_install_release() {
	[ "$REPOSITORY" = "https://github.com/pkyanam/pk.git" ] || return 1
	[ "$REF" = "main" ] || return 1
	command -v curl >/dev/null 2>&1 || return 1
	case "$(uname -s)" in
		Darwin) RELEASE_OS=darwin ;;
		Linux) RELEASE_OS=linux ;;
		*) return 1 ;;
	esac
	case "$(uname -m)" in
		x86_64|amd64) RELEASE_ARCH=amd64 ;;
		arm64|aarch64) RELEASE_ARCH=arm64 ;;
		*) return 1 ;;
	esac
	ASSET="pk_${RELEASE_OS}_${RELEASE_ARCH}.tar.gz"
	API_URL="https://api.github.com/repos/pkyanam/pk/releases/latest"
	if ! download_status "$API_URL" "$WORK_DIR/release.json" 1048576; then
		case "$DOWNLOAD_STATUS" in
			404) return 1 ;;
			*) return 2 ;;
		esac
	fi
	if ! RELEASE_INFO=$(bun -e 'const r=JSON.parse(await Bun.file(process.argv[1]).text());const a=process.argv[2];if(typeof r.tag_name!=="string"||!/^v[0-9A-Za-z][0-9A-Za-z.+_-]{0,63}$/.test(r.tag_name)||!Array.isArray(r.assets)||r.assets.some(x=>!x||typeof x.name!=="string"))process.exit(2);process.stdout.write([r.tag_name,r.assets.some(x=>x.name===a)?"yes":"no",r.assets.some(x=>x.name==="SHA256SUMS")?"yes":"no"].join("\n"))' "$WORK_DIR/release.json" "$ASSET"); then
		return 2
	fi
	TAG=$(printf '%s\n' "$RELEASE_INFO" | sed -n '1p')
	HAS_ASSET=$(printf '%s\n' "$RELEASE_INFO" | sed -n '2p')
	HAS_CHECKSUMS=$(printf '%s\n' "$RELEASE_INFO" | sed -n '3p')
	if [ "$HAS_ASSET" != yes ]; then
		return 1
	fi
	if [ "$HAS_CHECKSUMS" != yes ]; then
		return 2
	fi
	DOWNLOAD_BASE="https://github.com/pkyanam/pk/releases/download/$TAG"
	if ! download_status "$DOWNLOAD_BASE/SHA256SUMS" "$WORK_DIR/SHA256SUMS" 65536; then
		return 2
	fi
	if ! download_status "$DOWNLOAD_BASE/$ASSET" "$WORK_DIR/$ASSET" 268435456; then
		# Once metadata advertises this asset, a missing download is an error,
		# not evidence that no compatible release exists.
		return 2
	fi
	EXPECTED=$(awk -v name="$ASSET" '$2 == name || $2 == "*" name { print $1 }' "$WORK_DIR/SHA256SUMS")
	case "$EXPECTED" in
		[0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F]*) ;;
		*) return 2 ;;
	esac
	[ "${#EXPECTED}" -eq 64 ] || return 2
	ACTUAL=$(hash_file "$WORK_DIR/$ASSET") || return 2
	[ "$ACTUAL" = "$(printf '%s' "$EXPECTED" | tr 'A-F' 'a-f')" ] || return 2
	mkdir -p "$WORK_DIR/bootstrap"
	tar -xzf "$WORK_DIR/$ASSET" -C "$WORK_DIR/bootstrap" pk || return 2
	[ -f "$WORK_DIR/bootstrap/pk" ] && [ ! -L "$WORK_DIR/bootstrap/pk" ] || return 2
	chmod 700 "$WORK_DIR/bootstrap/pk"
	printf 'Installing verified pk release %s for %s/%s (Bun %s is required to run the UI)…\n' "$TAG" "$RELEASE_OS" "$RELEASE_ARCH" "$BUN_VERSION"
	"$WORK_DIR/bootstrap/pk" __install-release --archive "$WORK_DIR/$ASSET" --tag "$TAG" --sha256 "$ACTUAL" || return 2
	return 0
}

if try_install_release; then
	USER_HOME=$(cd ~ && pwd)
	BIN_DIR=${PK_BIN_DIR:-"$USER_HOME/.local/bin"}
	case ":${PATH:-}:" in
		*":$BIN_DIR:"*) ;;
		*) printf 'Add pk to PATH with: export PATH="%s:$PATH"\n' "$BIN_DIR" ;;
	esac
	exit 0
else
	RELEASE_RESULT=$?
	if [ "$RELEASE_RESULT" -eq 2 ]; then
		fail "prebuilt release installation failed verification or download; no source build was started"
	fi
	printf 'No compatible published binary release is available; falling back to a source build.\n'
fi

need_command git
need_command go

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

GIT_VERSION=$(git --version 2>&1) || fail "could not run git --version: $GIT_VERSION"

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
