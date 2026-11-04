# Preparing a release

The packaging workflow builds artifacts; it does **not** create a GitHub release,
push a tag, or publish binaries. Publication is a separate maintainer action.

## Local snapshot

From the repository root, using the toolchain in `go.mod`:

```sh
go test -race ./...
go vet ./...
go mod tidy -diff
go mod verify
go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
go run ./scripts/release --version v0.1.0-dev.1 --output dist
```

The output directory must not exist. Choose a new output directory for another
build; the packager does not clean or overwrite existing output.

Packaging builds macOS and Linux binaries for both ARM64 and AMD64, with CGO
disabled. Each archive includes `up`, README, the project license, and notices
for the union of runtime dependencies across the supported targets, including
the Go standard library. Missing dependency notices fail packaging.

The archives have fixed metadata; binaries use `-trimpath` and contain the
provided version plus Go's VCS build metadata. Building from a dirty checkout
does not make a release reproducible or establish a release source revision.
Use a clean, reviewed commit for a public release.

Expected files for `v0.1.0`:

```text
up_0.1.0_darwin_amd64.tar.gz
up_0.1.0_darwin_arm64.tar.gz
up_0.1.0_linux_amd64.tar.gz
up_0.1.0_linux_arm64.tar.gz
checksums.txt
```

Archives are unsigned and macOS binaries are not notarized. Do not describe
checksum verification as publisher authentication or code signing.

## Before tagging

1. Review the final source and release notes, and confirm a clean checkout.
2. Require green **CI** results for the exact commit on macOS and Linux,
   including the vulnerability job. Local success alone does not satisfy this.
3. Enable GitHub private vulnerability reporting and verify the reporting path
   described in `SECURITY.md`. Review repository protections and required checks.
4. Confirm the release scope and supported platforms. Smoke-test a clean native
   archive install: `--version`, `--help`, and `--demo --plain --yes`.
5. Obtain maintainer authorization to push the version tag and publish artifacts.

## Hosted packaging

The **Package release** workflow accepts either a pushed `v*` tag or a manual
snapshot version. Both paths rerun CI before producing archives and checksums.
Manual builds use the selected workflow revision and the supplied version;
they are snapshots, not proof that a matching source tag exists.

Download the workflow artifact and verify its checksums from inside its directory:

```sh
shasum -a 256 -c checksums.txt
```

On Linux, `sha256sum --check checksums.txt` is also supported. Extract the archive
matching the test machine into a new directory and run the packaged executable.
Test TUI review, cancellation, and terminal restoration. Test real upgrades only
with explicit authorization and record the package-manager versions and results.

## Publication

After the exact tag's workflow and archive checks pass, prepare a GitHub release
against that existing tag. Attach the four archives and `checksums.txt`, and
write notes covering supported adapters, known limits, and unsigned binaries.
Use a prerelease for `-rc` or other prerelease versions. Publishing is manual;
the workflows have read-only repository permissions.

Once the first release containing this rewrite is public, update the README's
development notice and document its actual tagged install command. Verify a
clean install from both the public tag and a downloaded archive before claiming
the release is available.
