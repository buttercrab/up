# up

One command to update your tools, with independent upgrades running in parallel.

`up` runs independent upgrades in parallel and shows a live terminal dashboard.
It discovers installed Homebrew, rustup, and npm commands on your PATH. Missing
tools are skipped. The review screen shows the commands before anything runs.

This rewrite is under development; its tagged binaries have not been published
yet. The source installation instructions below apply to a checkout containing
this implementation.

## Installation

From this checkout, with the Go toolchain specified in `go.mod`:

```sh
go install ./cmd/up
up --version
up --demo
```

Ensure Go's binary directory is on your PATH: `GOBIN`, or the `bin` directory
inside `GOPATH` if `GOBIN` is unset. Inspect both with `go env GOBIN GOPATH`.

Once a release containing this rewrite is published, download the archive for
your operating system and CPU from [GitHub Releases](https://github.com/buttercrab/up/releases).
Verify `checksums.txt` with `shasum -a 256 -c checksums.txt` on macOS or
`sha256sum --check checksums.txt` on Linux, then extract the matching archive and
put its `up` executable on your PATH. Archives are unsigned; macOS binaries are
not notarized. Windows is not supported.

## Build and run

Use Go 1.27 with the pinned Go 1.27.3 toolchain. The interface uses Bubble Tea v2,
Bubbles, and Lip Gloss; interactive commands use private pseudo-terminals.

```sh
go build -o /tmp/up ./cmd/up
/tmp/up --help
/tmp/up
/tmp/up --dry-run
/tmp/up --demo
```

In a terminal, `up` opens the review screen. Press **Enter** to start upgrades,
or **q** to leave without executing anything. The dashboard shows colored task
states, animated spinners, elapsed times, an overall completion bar, and live
logs. The bar counts finished tasks (including failures); it does not invent
package download percentages. Colors adapt to terminal capabilities and respect
`NO_COLOR`. To skip review and start in the dashboard:

```sh
/tmp/up --yes --jobs 4
```

This runs all detected adapters. Commands execute directly
without a shell. `--dry-run` always prints commands without executing them; it
does not refresh metadata or contact package registries. Preview describes the
command, not a package-level change list. Discovery may run bounded, read-only
`brew --prefix` and `npm prefix --global` queries to identify installation scope.

| Adapter | Command | Scope |
| --- | --- | --- |
| Homebrew | `brew upgrade --no-ask` | Outdated, unpinned formulae and casks |
| rustup | `rustup update` | Installed toolchains; rustup's configured self-update policy applies |
| npm | `npm update --global` | Global packages using npm's normal version policy; project dependencies are untouched |

Select tools explicitly or omit one:

```sh
/tmp/up --only brew,rustup
/tmp/up --exclude npm
```

An explicitly requested missing tool is an error. Independent installations run
concurrently up to `--jobs`. If npm, its Node runtime, or rustup resolves into a
Homebrew Cellar/Caskroom, that task shares Homebrew's resource lock and waits
while Homebrew runs. npm also shares the lock when its effective global prefix
overlaps Homebrew, even if npm uses an independent Node installation. Unknown
prefix metadata conservatively adds the lock, which can reduce parallelism.
Homebrew-managed rustup also receives `--no-self-update`, leaving its binary to
Homebrew. The dashboard labels shared-resource waits.

Versioned Homebrew executable paths use an equivalent stable `opt`/`bin` alias
when one exists. The selected Node lookup order is preserved when substituting
a matching versioned PATH entry. If a selected Homebrew upgrade could remove
an executable or runtime without a verified stable alias, discovery stops before
upgrades; use a stable entry point or exclude Homebrew from that run.

To see parallel activity without changing any installed tools:

```sh
/tmp/up --demo --yes --jobs 3
```

Demo mode uses three in-memory simulations with staggered progress. It never
executes package managers, even if they are installed. The dashboard labels it
**DEMO**. `--demo` cannot be combined with `--only` or `--exclude`.

Use `--plain --yes` for streaming plain output. Redirected output or non-terminal
input also selects plain mode; execution still requires `--yes`. Plain mode does
not support interactive prompts. `--yes` authorizes starting the plan; it does
not grant root privileges. Once the plan starts, adapters use native options
to avoid redundant upgrade confirmations: Homebrew uses `--no-ask`. `up` does not
send a blanket stream of `y` answers to arbitrary installer prompts. Unrecognized
questions and passwords still use terminal handoff.

## Dashboard controls and sudo

| Key | Action |
| --- | --- |
| Enter | Start the reviewed plan; close completed results |
| ↑/↓ or k/j | Select a task and its live log |
| PgUp / PgDn / Home | Browse log scrollback |
| End | Resume following live output |
| i | Connect to the selected running command's terminal |
| q / Esc / Ctrl-C | Leave review/results, or cancel a running plan |

Common password and yes/no prompts automatically suspend the dashboard and
connect your terminal to the **same running process**. For localized or unusual
prompts, press **i** manually. Only one command receives terminal input at a time;
other tasks continue independently. **Ctrl-]** returns to the dashboard without
stopping the command, and the dashboard returns automatically when it exits.

Homebrew continues to run as your user. If an installer invokes sudo, sudo reads
its password through its own terminal; `up` forwards keystrokes directly and
does not collect or log passwords. Password echo remains under the command's
control, as in a normal terminal. `up` never retries a partially completed
upgrade automatically or adds sudo to a command after a failure.

Full command output is saved in a private directory under the OS user cache:
`up/runs/run-*/001.log`. The dashboard shows the selected task's log path and a
concise failure cause when available. Logs can contain package-manager output with sensitive
information. They remain until removed manually or by OS cache cleanup.

Exit codes: `0` success or preview, `1` execution failure, `2` invalid arguments,
`130` cancellation. In the dashboard, Ctrl-C cancels active commands and queued
work; during handoff it goes to the connected command. On macOS and
Linux, commands run in separate process groups; cancellation sends TERM, then
KILL after two seconds. Descendants that deliberately leave the process group
are outside this cancellation boundary.
Cancellation remains active while output drains after the parent exits, and
remaining descendants retain their configured termination grace. Output-drain
timeouts clean up the group without discarding the parent's nonzero exit code.
Log-write failures remain execution failures, including in the TUI.
An output destination that blocks indefinitely can still delay shutdown.

## Architecture

```text
cmd/up/                 entry point and signal handling
internal/app/           flags, planning, plain output, exit codes
internal/ui/            review, live dashboard, results, terminal handoff
internal/report/        safe log text, failure causes, recovery hints
internal/update/        plans, scheduler, results, execution snapshots
internal/process/       subprocess execution and process-group cancellation
internal/updaters/      discovery, selection, Homebrew/runtime resource conflicts
internal/updaters/brew/ Homebrew adapter and native confirmation policy
internal/updaters/rustup/ Rust toolchain adapter
internal/updaters/npm/  global npm adapter
internal/demo/          no-change concurrent task simulation
```

An updater implements `Plan(context.Context) (update.Plan, error)` without
changing installed packages. A plan names its commands and exclusive resources.
Commands within a plan run sequentially; plans sharing any resource never
overlap. The scheduler starts other available plans up to the job limit and
continues independent work after a failure. Resource locks apply within a single
run; coordinating multiple `up` processes is not implemented.

`Engine.Start` validates and copies plans before execution. Its returned run
exposes `Snapshot`, `Done`, and `Wait`. Snapshots include queued/running/final
states, timestamps, errors, process exit codes when available, and a bounded
64 KiB log tail per task. The TUI polls snapshots without blocking the
runner. Full logs are optional for library callers and enabled by the CLI.

Adapters should declare shared resources consistently (for example, `homebrew`)
and pass arguments separately rather than constructing shell command strings.
No external plugin protocol or public Go API is promised at this stage.

## Verification

```sh
go test -race ./...
go vet ./...
```

Tests use controlled executors, helper subprocesses, and a fake `brew` executable.
They do not run the installed package manager or upgrade host packages.
Interactive tests need access to `/dev/tty`. They exercise the actual dashboard,
non-echoing password prompts, detach/reattach, cancellation, and terminal restoration.

CI runs formatting, module metadata verification, vet, race-enabled tests, and
harmless demo smoke tests on macOS and Linux. A separate job checks reachable
vulnerabilities with a pinned `govulncheck` version. Tagged builds and manual
snapshots can produce versioned archives and checksums after these checks pass;
the packaging workflow does not publish a release.

See [CONTRIBUTING.md](CONTRIBUTING.md), [SECURITY.md](SECURITY.md), and
[the release guide](docs/releasing.md) for development and release procedures.
