#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
TMP=$(mktemp -d "${TMPDIR:-/tmp}/pk-installer-test.XXXXXX")
cleanup() { rm -rf "$TMP"; }
trap cleanup EXIT HUP INT TERM

BUN=$(command -v bun) || { echo 'bun is required for this fixture' >&2; exit 1; }
REAL_PATH=$(dirname "$BUN"):$PATH
mkdir -p "$TMP/fixture" "$TMP/mock-bin" "$TMP/archive-root"
cat > "$TMP/archive-root/pk" <<'STUB'
#!/bin/sh
[ "${1:-}" = __install-release ] || exit 90
printf '%s\n' "$*" > "$PK_FIXTURE_MARKER"
STUB
chmod 755 "$TMP/archive-root/pk"
tar -czf "$TMP/fixture/pk_darwin_arm64.tar.gz" -C "$TMP/archive-root" pk
ASSET=pk_darwin_arm64.tar.gz
HASH=$(shasum -a 256 "$TMP/fixture/$ASSET" | awk '{print $1}')
printf '%s  %s\n' "$HASH" "$ASSET" > "$TMP/fixture/SHA256SUMS"
cat > "$TMP/fixture/release.json" <<'JSON'
{"tag_name":"v0.0.0-installer-test","assets":[{"name":"pk_darwin_arm64.tar.gz"},{"name":"SHA256SUMS"}]}
JSON

cat > "$TMP/mock-bin/uname" <<'MOCK'
#!/bin/sh
case "${1:-}" in
  -s) echo Darwin ;;
  -m) echo arm64 ;;
  *) exit 2 ;;
esac
MOCK
cat > "$TMP/mock-bin/curl" <<'MOCK'
#!/bin/sh
output=
url=
https_proto=no
https_redirect=no
max_size=no
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output) output=$2; shift 2 ;;
    --write-out) shift 2 ;;
    --proto) [ "$2" = '=https' ] || exit 31; https_proto=yes; shift 2 ;;
    --proto-redir) [ "$2" = '=https' ] || exit 32; https_redirect=yes; shift 2 ;;
    --max-filesize) [ "$2" -gt 0 ] || exit 33; max_size=yes; shift 2 ;;
    --*) shift ;;
    *) url=$1; shift ;;
  esac
done
[ "$https_proto" = yes ] && [ "$https_redirect" = yes ] && [ "$max_size" = yes ] || exit 34
case "$url" in
  https://api.github.com/repos/pkyanam/pk/releases/latest) source=$PK_FIXTURE_DIR/release.json ;;
  */SHA256SUMS) source=$PK_FIXTURE_DIR/SHA256SUMS ;;
  */pk_darwin_arm64.tar.gz)
    if [ "${PK_FIXTURE_MISSING_ARCHIVE:-}" = yes ]; then printf 404; exit 0; fi
    source=$PK_FIXTURE_DIR/pk_darwin_arm64.tar.gz ;;
  *) exit 22 ;;
esac
cp "$source" "$output"
printf 200
MOCK
cat > "$TMP/mock-bin/go" <<'MOCK'
#!/bin/sh
: > "$PK_FIXTURE_FORBIDDEN"
exit 91
MOCK
cat > "$TMP/mock-bin/git" <<'MOCK'
#!/bin/sh
: > "$PK_FIXTURE_FORBIDDEN"
exit 92
MOCK
chmod 755 "$TMP/mock-bin/"*

export PK_FIXTURE_DIR="$TMP/fixture"
export PK_FIXTURE_MARKER="$TMP/installed"
export PK_FIXTURE_FORBIDDEN="$TMP/source-fallback-used"
export PK_BIN_DIR="$TMP/bin"
export PK_LIB_DIR="$TMP/lib"
PATH="$TMP/mock-bin:$REAL_PATH"; export PATH

sh "$ROOT/install.sh" > "$TMP/success.out" 2> "$TMP/success.err"
grep -q '__install-release --archive' "$PK_FIXTURE_MARKER"
[ ! -e "$PK_FIXTURE_FORBIDDEN" ]

rm -f "$PK_FIXTURE_MARKER"
PK_FIXTURE_MISSING_ARCHIVE=yes; export PK_FIXTURE_MISSING_ARCHIVE
if sh "$ROOT/install.sh" > "$TMP/missing.out" 2> "$TMP/missing.err"; then
  echo 'installer unexpectedly fell back after an advertised archive returned 404' >&2
  exit 1
fi
grep -q 'no source build was started' "$TMP/missing.err"
[ ! -e "$PK_FIXTURE_FORBIDDEN" ]
unset PK_FIXTURE_MISSING_ARCHIVE

printf '%064d  %s\n' 0 "$ASSET" > "$TMP/fixture/SHA256SUMS"
if sh "$ROOT/install.sh" > "$TMP/failure.out" 2> "$TMP/failure.err"; then
  echo 'installer unexpectedly accepted a checksum mismatch' >&2
  exit 1
fi
grep -q 'no source build was started' "$TMP/failure.err"
[ ! -e "$PK_FIXTURE_FORBIDDEN" ]
printf 'installer prebuilt fixture passed (success, missing advertised archive, and checksum-failure paths)\n'
