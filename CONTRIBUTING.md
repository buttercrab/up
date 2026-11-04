# Contributing to up

Keep changes small, keep execution separate from rendering, and make updater
behavior visible before it runs. The project uses the MIT license in `LICENSE`.

## Development

Use the Go toolchain specified in `go.mod` on macOS or Linux. From a checkout:

```sh
go mod download
go run ./cmd/up --demo
go test -race ./...
go vet ./...
go mod tidy -diff
go mod verify
```

Format Go files with `gofmt`. Interactive tests require pseudo-terminal access
and `/dev/tty`; they use fake commands and fixture passwords, not installed
package managers or real credentials. Do not replace terminal coverage with
skipped tests when investigating a platform failure.

Use `--demo` for UI work. Running `up` and approving its plan changes the host's
installed tools. A successful demo is not evidence that real package upgrades
work. Record any separately performed package-manager smoke tests explicitly.

## Adding an updater

1. Add a package under `internal/updaters` implementing
   `Plan(context.Context) (update.Plan, error)`. Planning must not change packages.
2. Declare its capability in `internal/inventory.Catalog()` and register its
   implementation in `internal/updaters/registry.go`. Mark detection-only tools
   unsupported until an adapter exists. Document detection, upgrade scope,
   native confirmation options, and runtime dependencies.
3. Declare exclusive resources for anything it can mutate or replace. A task
   using a Homebrew-managed runtime must not race Homebrew replacing that runtime.
4. Add command fixtures covering successful execution, failures, missing tools,
   and shared-resource behavior. Tests must never invoke host upgrades.
5. Update CLI help, README examples, and any selection tests affected by discovery.

Pass command arguments separately. Never construct shell commands from user
input. Keep privilege decisions explicit: do not prepend sudo, answer arbitrary
prompts with `y`, retry partially completed upgrades, or capture passwords.
Use package-manager-native confirmation options for approved routine upgrades;
other prompts belong to terminal handoff.

## Pull requests

Explain the observable change and why it is needed. Include the checks you ran,
their results, and remaining limits. Distinguish local tests, hosted CI, real
package-manager execution, and published releases. Redact logs before sharing.
Report security issues according to [SECURITY.md](SECURITY.md).

Treat contributors respectfully. Discuss concrete behavior and evidence, and
keep disagreement focused on the work.

For release preparation and publication boundaries, see
[docs/releasing.md](docs/releasing.md).
