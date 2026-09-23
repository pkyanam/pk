#!/bin/sh
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/../../.." && pwd)
OUTPUT="$SCRIPT_DIR/agentmail-worker"
if [ "$(go env GOOS)" = "windows" ]; then
  OUTPUT="$OUTPUT.exe"
fi

cd "$REPO_ROOT"
go build -o "$OUTPUT" ./examples/plugins/agentmail
printf 'Built AgentMail worker: %s\n' "$OUTPUT"
