# Install and update

## Install

Run the installer from a shell:

```sh
curl -fsSL https://raw.githubusercontent.com/pkyanam/pk/main/install.sh | bash
```

The installer uses a checksum-verified paired Go/UI release when GitHub has a
compatible published asset. The current release workflow builds macOS arm64 and
Linux amd64 archives. Bun remains required to run the OpenTUI interface, even
when installing a prebuilt archive; Go and Git are needed only for a source
build.

If no release exists yet or no asset matches the current platform, the installer
announces that it is falling back to a source build. That path fetches the
official repository and requires Git plus Go 1.27 or newer (or Go 1.21 or newer
with automatic toolchain selection enabled). If release metadata, the archive,
or its checksum cannot be downloaded or verified, installation stops instead
of silently switching to a source build. The installer does not install system
packages.

The paired release includes the Go executable and built UI assets. Bun itself
is not bundled. The installer preserves an existing managed release for
rollback and honors `PK_BIN_DIR` and `PK_LIB_DIR`.

## Update

From an installed version of pk:

```sh
pk update
pk version
```

`pk update` checks for the latest published compatible paired release and
verifies its SHA-256 checksum before activation. When no release or matching
platform asset is available, it prints a source-build fallback notice and uses
the official source. A network, archive, or checksum error aborts the update;
the active release remains in place. Bun must be installed before a paired
release can be activated. Source fallback also requires the source-build
toolchain described above.

For development, explicitly select a checkout to test and install:

```sh
pk update --source /path/to/pk
```

This validates and builds that source tree in a staging copy. It is separate
from the normal published-release path. The TUI provides the same operations
with `/update [--source PATH]` and `/rollback`. Updates and rollback require an
idle foreground session; `/reload` restarts pk on the activated release and
restores the saved session. Use `/new` to start with the current prompt and
tool configuration. If the terminal cannot restart the process cleanly, exit
and launch pk again.

`pk rollback` restores the prior managed release. `pk version` reports the
active release and its provenance. Publishing a release depends on the
repository's release workflow; this documentation does not imply that a
compatible release is already available.
