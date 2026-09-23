#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
tmp=$(mktemp -d "${TMPDIR:-/tmp}/pk-acp-sdk-smoke.XXXXXX")
cleanup() {
  python3 -c 'import shutil,sys; shutil.rmtree(sys.argv[1], ignore_errors=True)' "$tmp"
}
trap cleanup EXIT HUP INT TERM

mkdir -p "$tmp/private-home" "$tmp/client"
npm install --prefix "$tmp/client" --no-save --package-lock=false @agentclientprotocol/sdk@1.5.0
go build -o "$tmp/fake-agent" "$repo_root/scripts/acp-sdk-smoke/fake-agent.go"
node "$repo_root/scripts/acp-sdk-smoke/client.mjs" \
  "$tmp/client/node_modules/@agentclientprotocol/sdk/dist/acp.js" \
  "$tmp/fake-agent" "$tmp/private-home"
