#!/bin/sh
set -eu

if [ "$#" -ne 1 ]; then
  echo "usage: reset.sh /tmp/pk-demo-fixture.XXXXXX" >&2
  exit 2
fi
python3 - "$1" <<'PY'
import os
import pathlib
import shutil
import sys

root = pathlib.Path(sys.argv[1]).resolve(strict=True)
if not root.name.startswith("pk-demo-fixture.") or not root.is_dir():
    raise SystemExit("refusing reset: target is not a generated demo fixture directory")
marker = root / ".pk-demo-fixture"
if not marker.is_file() or marker.read_text() != "pk-demo-fixture-v1\n":
    raise SystemExit("refusing reset: demo ownership marker is missing or invalid")
owned_files = {
    ".pk-demo-fixture", "README.md", "go.mod",
    "intervals/coverage.go", "intervals/coverage_test.go",
    "cmd/covercount/main.go",
}
found = set()
for current, dirs, files in os.walk(root, followlinks=False):
    current_path = pathlib.Path(current)
    for name in dirs:
        path = current_path / name
        if path.is_symlink():
            raise SystemExit(f"refusing reset: unexpected symlink {path.relative_to(root)}")
    for name in files:
        path = current_path / name
        if path.is_symlink():
            raise SystemExit(f"refusing reset: unexpected symlink {path.relative_to(root)}")
        found.add(path.relative_to(root).as_posix())
if found != owned_files:
    extra = sorted(found - owned_files)
    missing = sorted(owned_files - found)
    raise SystemExit(f"refusing reset: workspace contents differ (extra={extra}, missing={missing})")
shutil.rmtree(root)
print(f"Removed owned demo fixture: {root}")
PY
