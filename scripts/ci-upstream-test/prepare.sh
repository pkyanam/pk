#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: $0 MODULE_CACHE_DIR ISOLATED_DESTINATION" >&2
  exit 2
fi
source_dir=$(cd "$1" && pwd)
destination=$2
patch_file="$(cd "$(dirname "$0")" && pwd)/fixture-race.patch"

if [[ ! -f "$source_dir/go.mod" ]] || ! grep -Fqx 'module github.com/unreallabsai/unreal-agent' "$source_dir/go.mod"; then
  echo "expected pinned Unreal Agent module in $source_dir" >&2
  exit 1
fi
if [[ -e "$destination" ]]; then
  echo "isolated destination already exists: $destination" >&2
  exit 1
fi

# Prove the narrowly scoped fixture patch applies exactly before copying. This
# intentionally refuses upstream drift instead of fuzzing or patching a new test.
patch --dry-run --batch --forward --fuzz=0 -p1 -d "$source_dir" < "$patch_file"
mkdir -p "$destination"
cp -R "$source_dir/." "$destination/"
chmod -R u+w "$destination"
patch --batch --forward --fuzz=0 -p1 -d "$destination" < "$patch_file"

if ! grep -Fq 'var called atomic.Bool' "$destination/cmd/internal/agentrunner/args_test.go"; then
  echo "fixture race patch verification failed" >&2
  exit 1
fi
if [[ "$source_dir" == "$destination" ]]; then
  echo "isolated copy must not point to module cache" >&2
  exit 1
fi
printf 'Prepared isolated test copy: %s\nPinned source left unchanged: %s\n' "$destination" "$source_dir"
