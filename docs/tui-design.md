# Terminal design

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
