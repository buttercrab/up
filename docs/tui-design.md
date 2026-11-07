# Terminal design

## Design with Intent: failure attention

Applied the `design-with-intent` skill from the user's local skill library on
2026-11-07. The settled brief is a keyboard-operated terminal application for
reviewing and running parallel tool updates. The user specifically chose:
**keep the current view when another update fails, and flag the failure**.
The existing visual direction remains in place for this bounded revision.

The library was surveyed across skills, documented flows, patterns and
foundations, including queries for parallel work, terminal interfaces,
monitoring, triage and failures. No direct terminal match was found, so the
search extended to first-party Lazygit keybinding documentation. Sources below
were read as documentation; no third-party visual style or authenticated flow
is claimed as inspected or copied.

| Resource | Fit and transfer | Mismatch and boundary |
| --- | --- | --- |
| [GOV.UK error summary](https://design-system.service.gov.uk/components/error-summary/) | A visible, navigable map of failures. The summary names failed tools and offers `f inspect`. | Its form-validation focus transfer contradicts the user's monitoring preference. No automatic focus movement or form styling is adopted. |
| [Lazygit keybindings](https://github.com/jesseduffield/lazygit/blob/master/docs/keybindings/Keybindings_en.md) | A real terminal reference for deliberate navigation and separate content scrolling. Existing arrows/PageUp/PageDown remain; `f` explicitly cycles failures. | Git-specific actions and key assignments are not imported; no claim of visual or live interaction inspection. |
| [Carbon empty states](https://www.carbondesignsystem.com/building-blocks/core/patterns/empty-states) | Distinguish pending output from completed work with no captured output. | A tool process failure is not an empty search result. No illustrations, generic retry button or promise of successful recovery. |
| Linear Triage library card | Considered for explicit disposition of unresolved work. | Snooze/decline/duplicate states do not fit package-manager outcomes, so those mechanisms were rejected. |

The implementation keeps selection and scrollback when background failures
arrive. A summary remains visible while the user reads another tool. Pressing
`f` is an explicit request to open the next failed tool's logs and cause; it
never executes a command or retries a partially completed upgrade. Repeated
presses cycle actual failures. Without failures there is no failure action.

Checks cover a real engine failure arriving while another tool's log is paused,
keyboard failure navigation, failure-free and no-output states, and bounded
wide/narrow rendering. These establish the implemented interaction, not a
measured usability improvement or a universal aesthetic rule.

## Existing visual direction

`up` is an operations console: choose work, approve its commands, follow the
result, and handle input when a package manager needs it. Layout follows those
actions rather than presenting an overview of decorative metrics.

This design applies patterns from the local Taster repository. Its browser
detector does not inspect Go terminal views, so this is a manual adaptation,
not a detector score or a generated web design system.

| Finding | Taster reference | Terminal change |
| --- | --- | --- |
| Purple/green gradient and full-width completion track dominated the screen | COL-01; progress-and-spinner; token-themes | Compact finished/running/waiting counts; flat bars only for measured downloads |
| Every running state and download repeated the accent color | AC-07, AC-08; status-label | Neutral state words; outcome color on task symbols and actual errors |
| A large rounded frame enclosed sparse content | CMP-02; spacing-and-dividers; master-detail-split | One surface; separate tool list and selected tool detail with space |
| Package labels shifted the progress counters horizontally | hairline-data-table; readable-at-every-width | Aligned columns on wide terminals; stacked records on narrow terminals |
| Repeated headings, worker counts, instructions and zero-valued colored summaries | dashboard-zones; plain-honest-copy | Short route heading, actionable menu descriptions, context-sensitive key hints, no colored zero failures |

## Shared system

The eight Taster weighting axes are translated to terminal constraints:

- Density: compact records, one row per tool; space between sections.
- Contrast: ordinary terminal text, muted metadata, bold section titles.
- Roundness: none; section grouping does not require frames.
- Type expression: inherit the terminal font; no banners or all-caps labels.
- Color restraint: neutral emphasis; success/error colors represent outcomes.
- Elevation: one terminal surface, no simulated cards or shadows.
- Motion: existing spinners indicate running work; numeric bars change with data.
- Stroke: flat download tracks; no surrounding box or repeated dividers.

`internal/ui/ui.go` owns the light/dark semantic roles. Home, inventory, command
review, execution, and result screens share these roles. Text and key actions
carry meaning without color. No terminal background or font is forced.

## Interaction and evidence

The home menu keeps Upgrades, Inventory, and Demo visible. Enter opens a feature;
up/down selects a tool, `p` switches progress/logs, `i` hands off the terminal,
and Esc cancels/backtracks through the existing cleanup path. Approval and
execution behavior are unchanged.

The selected tool's detail owns package progress or logs, not both repeated in
several panels. Package names and versions remain visible; long names wrap.
Only reported byte totals produce percentages. A download is not an installed
package, and command exit status remains authoritative.

Validation uses real Go renderer snapshots at 80 and 116 columns, responsive
geometry tests, light-mode contrast checks, and real PTY flows for selection,
resize, prompts, cancellation, return navigation, and demo execution. Browser
previews display captured renderer output with sample data; they do not prove
a real package upgrade succeeded.
