#!/bin/sh
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
parent=${1:-${TMPDIR:-/tmp}}
mkdir -p "$parent"
workspace=$(mktemp -d "$parent/pk-demo-fixture.XXXXXX")
cp -R "$script_dir/template/." "$workspace/"
printf '%s\n' 'pk-demo-fixture-v1' > "$workspace/.pk-demo-fixture"
printf 'Created disposable coding fixture: %s\n' "$workspace"
printf '\nThe interval tests intentionally fail on inclusive endpoints. Start pk in this directory and ask it to fix the bug, run go test ./..., and verify the CLI example in README.md.\n'
