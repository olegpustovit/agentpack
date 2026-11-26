# Gateway design system — Folio kit

The Folio kit for the gateway web UI: a tinted **canvas** for navigation (toolbar, sidebar) and one raised **page** for the work. Tokens live in `ui/src/index.css`; components live in `ui/src/components/kit/` (composed from shadcn primitives on `@base-ui/react` in `ui/src/components/ui/`). Routes and screens are in `AGENTS.md` (Gateway UI) and `ui/SURFACE.md`.

## Token layers

`ui/src/index.css` defines tokens in three layers. Components read layers 2 and 3 only (never hex values or primitives).

1. **Primitives** — Neutral graphite ramp `--n-0` … `--n-1000` (hue 265, chroma < 0.01), semantic hues (`--blue-500/300`, `--green-500/300`, `--amber-500/300`, `--red-500/300`), and `--ink`.
2. **Semantic** — Each color resolves per `color-scheme` via `light-dark()` (no duplicated dark override blocks):
   - **Surfaces:** `--canvas` (`--n-50` / `--n-1000`) holds the toolbar and sidebar; `--page` (`--n-0` / `--n-900`) is the one raised work surface (`--r-page` 12 px, `--e-page`); `--raised` (`--n-0` / `--n-850`) for popovers, menus, and sheets; translucent `--sunken` for tracks, search, and code; `--control-fill` and `--field-fill`.
   - **Text & lines:** `--text-1`, `--text-2`, `--text-3` (AA ≥ 5.0:1 at 11 px on both surfaces); `--line` and `--line-soft` (0.5 px hairlines only); `--hover`, `--press`.
   - **Accents & status:** `--accent` (text, links, bars), `--accent-fill` (any fill under `--on-accent` text; ≥ 4.5:1 in both schemes), `--on-accent`, `--accent-tint`, `--control-edge` (checkbox and field boundaries; ≥ 3:1), `--ok`, `--warn`, `--danger`, `--idle`, plus legible word tones (`--ok-word`, `--warn-word`, `--danger-word`) and diff fills (`--add*`, `--del*`).
   - **Elevation:** `--e-ctl`, `--e-page`, `--e-pop`, `--e-sheet` (0.5 px rim + contact shadow). Blur stays on menus (`--menu`) only — never on surfaces content scrolls under.
   - **Geometry & rhythm:** Space steps `--s1…--s9` (2, 4, 6, 8, 12, 16, 20, 24, 32 px), `--gutter` 8 px, dialog `--dlg-pad` 20 px and `--dlg-gap` 16 px. Concentric radii: `--r-xs` 4, `--r-row` 6, `--r-ctl` 7, `--r-pop` 10, `--r-page` 12, `--r-sheet` 16, `--r-pill` 999 px.
   - **Charts:** `--series-1…--series-6` (Claude 1, Codex 2, Grok 3, Cursor 4, OMP 5, other 6 = `--fg3`). Bars are thin and rounded, average is a dashed `--fg3` rule, axis sits on the right; the Usage heatmap uses a neutral graphite ramp.
3. **Density & safe areas** — Compact defaults switch to touch under `@media (max-width: 760px), (pointer: coarse)` or `:root[data-density=touch]`. `--safe-t/b/l/r` read `env(safe-area-inset-*, 0px)`. At `≤ 760 px` the window flattens to one full-bleed surface (`--canvas: var(--page)`) with a faint `--chrome` tint on the toolbar and tab bar.

**Legacy aliases & shadcn mapping:** Legacy names (`--window`, `--chrome`, `--sidebar`, `--control`, `--field`, `--track`, `--fg*`, `--sep*`, `--fs*`, `--shadow-*`) alias layer 2 so existing components keep working until migrated. shadcn tokens map onto the kit tokens; because `--accent` collides, `@theme inline` points `--color-accent` at `--accent-ui` (`--accent-soft`) and `--color-primary` at `--primary` (`--accent-fill`). All base rules live inside `@layer base` so utilities always win.

## Metrics

| Element | Compact (desktop) | Touch (`≤ 760 px` or `pointer: coarse`) |
|---|---|---|
| Toolbar (`--bar-h`) | 48 px | 52 px + `--safe-t` |
| Tab bar (`--tab-h`) | hidden | 52 px + `--safe-b` |
| Type (`--t-caption` / `footnote` / `body` / `title` / `large`) | 11 / 12 / 13 / 15 / 20 px | 12 / 14 / 16 / 17 / 24 px (`input/textarea/select` ≥ 16 px prevents iOS zoom) |
| Control (`--ctl-h` / `--ctl-h-sm`) | 26 / 22 px | 40 / 32 px |
| Row (`--row-h` / `--row-h-2`) | 28 / 40 px | 44 / 58 px |
| Group header (`--head-h`) | 26 px | 34 px |
| Radius (`--r-ctl` / `--r-row` / `--r-page` / `--r-sheet`) | 7 / 6 / 12 / 16 px | 10 / 10 / 12 / 16 px |
| Sidebar / List pane | 264 px (220–420 px) / 300 px | full pane |
| Tooltip delay | 700 ms | none |

Separators are 0.5 px. Panes scroll independently; there is no page scroll and no bottom status bar.

### Dialog metrics

- **Inset & spacing:** `--dlg-pad` 20 px on every edge and column (plus `--safe-b` on phone); `--dlg-gap` 16 px between sections; title → description 4 px (CSS adjacent rule on `[data-slot=dialog-title] + [data-slot=dialog-description]`).
- **Widths:** S 360 px (`w-90`, e.g. Rename), M 480 px (`w-120`, default), L 640 px (`w-160`, Project settings), XL 900 px (New session). Phone renders full-width bottom sheets capped at `100dvh − --safe-t − --s4`.
- **Footer (`SheetActions`):** Pinned sticky to the bottom with a 0.5 px hairline (`pinned={false}` when a sheet provides its own footer), optional left status line, Cancel + primary right (`min-w-20`); 44 px full-width buttons on phone clearing `--safe-b`. No close `✕`.
- **Alerts (`ConfirmAlert`):** 300 px (`w-75`) centered on `--raised` + `--e-sheet`. Cancel receives initial focus and comes first in both the desktop button row and the phone stack of 44 px buttons.

## Kit components

Components live in `ui/src/components/kit/` (shell pieces in `ui/src/features/shell/`).

| Component | Built on | Rules |
|---|---|---|
| `AppWindow` | CSS grid | 3 rows: toolbar, content, phone tab bar on `--canvas`. No bottom status bar |
| `ListDetail` | `ResizablePanelGroup` | Desktop wraps content in one raised `[data-slot=page]` (`bg-(--page)`, `--r-page`, `--e-page`, inset by `--gutter`). Phone switches pane by route |
| `Toolbar`, `ToolbarButton` | `Button` | Unified with `--canvas`, pads for `--safe-*` and Chrome/Edge `display-mode: window-controls-overlay` (`app-region: drag` / `no-drag`). Leading: sidebar toggle, capsule view switcher (Sessions, Library, System; ⌘1–3, Ctrl off Mac), title (`--t-title`). Trailing: connection indicator, activity viewer, route tools, Command palette |
| `ConnectionItem` | `Popover` | Appears when the gateway is unreachable, a machine is offline, or live updates pause. The menu shows the last live update and offers Reconnect alongside the machine list |
| `ActivityViewer` | `Button` + lazy `Popover` | Success shows for 4 s then folds into a history icon. The history keeps the last 20 successes below unresolved failures, with Retry and Dismiss |
| `SourceList`, `SourceRow`, `GroupHeader` | plain + roving tabindex | Row is `<Link>` or button. Selected row rises to `--page` with `--e-ctl` when unfocused and `--accent-fill` when the list is focused. Trailing `⋯` visible on hover or selection. Finder-style marking (⌘/Ctrl-click, Shift-click, Esc clears). `NotifyOffer` sits on the Needs-you header. `SystemSidebar` (`features/system/`) is the one list for System: Usage, Limits, settings grouped by scope, then Topology and machines, with one search |
| `TabBar` | `<nav>` | Phone only (`≤ 760 px`), 3 tabs (the System tab opens Usage; tapping it again opens the System list), height `calc(--tab-h + --safe-b)`. Sessions tab carries an amber Needs-you badge. Session details use the full height below the toolbar |
| `SegmentedControl` | `ToggleGroup` | `--ctl-h` track (`--sunken`), `--ctl-h-sm` thumb (`--control-fill` + `--e-ctl`) |
| Sliding thumb | CSS `::before` | Fills the selected segment or toolbar view link using its measured offset and width |
| `FilterButton` | `Button` | Opens view options and uses `--accent-tint` and `--accent` while filters are active |
| `FilterFacet` | named group | Groups filter options by dimension and offers Any to clear that dimension |
| `FilterChip` | toggle button | Shows an option and its count, dims unpressed zero counts, and tints pressed options |
| `FilterToken` | two buttons | Shows the active value, labels its dimension, and offers separate edit and remove actions |
| `SearchField` | `Input` | `--sunken` fill, inset icon, `⌘F` focuses it |
| `Field`, `FieldGrid` | `Input`, `Select` | 120 px right-aligned labels, 12 px row gap (16 px stacked on phone) |
| `FormGroup` | `<section>` | Inset rows inside a 0.5 px outline with `--r-pop` radius. Optional heading and action above, footer below. |
| `FormRow` | flex | Label and description lead, control aligns to the trailing edge. Uses `--row-h-2` minimum height, `--s5` side padding and 0.5 px dividers. Optional details span the row below. |
| `Sheet`, `SheetActions` | `Dialog` | Drops 8 px from toolbar on desktop; bottom sheet on phone. Follows Dialog metrics above |
| `ConfirmAlert` | `AlertDialog` | 300 px centered; title quotes the object name. Follows Dialog metrics above |
| `Menu`, `RowContextMenu` | `DropdownMenu`, `ContextMenu` | macOS 26 NSMenu: `--menu` translucent material, 12 px corners, 5 px padding, `--ctl-h` rows with 6 px pill, separators inset 16 px. `danger` rows are red and highlight `--menu-danger` |
| `PopupSelect` | `Select` | Replaces native `<select>`. Menu surface with left check column; `danger` items get their own group and turn the trigger red with a shield |
| `Banner` | plain | Inside a pane, top of its scroller. Icon, one sentence, optional action. Tones: `info`, `warn`, `danger` |
| `StatusDot` | plain | See State vocabulary. Always paired with a word or `aria-label` + tooltip |
| `Inspector`, `InspectorHead`, `KeyValue`, `Section` | plain | Detail views cap at 1180 px, two columns at ≥ 1260 px. Session `ConversationInspector` sits inside the page as an Xcode-style ruled column: no outer padding, 12 px (`--s5`) side inset per section, full-width 0.5 px dividers between sections, none above the first section (16 px top inset) |
| `DataGrid` | `<table>` | Sticky `--head-h` header, `--row-h` rows, fixed layout, resizable columns |
| `Meter` | `<meter>` | 6 px rounded bar, accent fill; warn ≤ 20% left, danger ≤ 5%. Hidden when unknown |
| `Hash` | `<code>` | First 7 characters, full value in tooltip, click copies silently |
| `Empty`, `RowSkeleton`, `Spinner` | plain | Empty says what appears and how. Spinner respects reduced motion |
| `Kbd` | plain | `--t-caption`, muted; hidden automatically on `@media (hover: none)` |

## Motion

Three durations (`--dur-1` 120 ms, `--dur-2` 200 ms, `--dur-3` 320 ms) and one easing curve (`--ease: cubic-bezier(.2, .8, .2, 1)`). Popup entry starts at `--enter-scale` (0.98) and `--enter-shift` (4 px). Progress spinners can loop. `prefers-reduced-motion: reduce` disables all animation and transitions.

| Surface | Motion |
|---|---|
| Activity viewer | Fades in 3 px (`--dur-2`); text folds into history icon (`--dur-3`) |
| Sheets | Drop 8 px from toolbar (`--dur-2`); slide from bottom edge on phone |
| Usage charts & heatmap | Bars grow from baseline once per range change (`--dur-3`); heatmap cells fade in |
| Popup entry | Fade, scale and shift (`--dur-1`). Anchored popups shift away from their anchor as they enter |
| Segments and toolbar views | The thumb starts under the selected item. Position and width slide on later selections (`--dur-2`) |
| Toolbar view changes | The new pane fades in and settles from `--enter-shift` (`--dur-2`) |
| Rows | Selection is instant |

## State vocabulary

Use these exact words everywhere.

| State | Glyph | Word | On selected row (focused) |
|---|---|---|---|
| Running | filled green dot | Running | white dot |
| Stopped | hollow ring | Stopped | white ring |
| Idle | hollow green ring | Idle | white ring |
| Restarting | spinner | Restarting | white spinner |
| Offline | dashed ring | Offline | white dashed ring |
| Read-only | filled gray dot | Read-only | white dot |
| Needs update | filled amber dot | Needs update | white dot |
| Needs you | filled amber dot | Needs you | white dot |

## Alert model

Each situation has one surface. Never float toasts over content, and never use `notice()`, `*-status` lines, or `window.confirm`.

| Situation | Surface | Example copy |
|---|---|---|
| Action finished and result is on screen | Nothing: the row, switch, or list shows it | Row reads `Stopped` |
| Action finished out of sight | `ActivityViewer` in the toolbar (4 s, then folds into history icon) | `Updating mac-mini to 0.14.2` |
| Copied | The control shows a check or `Copied` for 1.5 s (`useCopied`) | `Copy` → `Copied` |
| Action failed with nothing open | `ActivityViewer` in the toolbar, stays until dismissed, with `Retry` | `Couldn't stop claude on oracle-vm: worker failed` |
| Ongoing state of a pane or terminal | `Banner` inside the pane | `mac-studio is offline. Showing its last known sessions` |
| Gateway unreachable or peer offline | `ConnectionItem` in the toolbar | `1 machine offline` |
| Irreversible or disruptive action | `ConfirmAlert` (quoted object name) | Title `Stop “Triage…”?` Body `In acme-web on studio. The session stays in the list…` |
| Form validation or server rejection | Inline `FieldError` under the field | `Enter a name.` |

Errors say what failed and what to do next. Do not write `Error:`, `Oops`, or apologies.
