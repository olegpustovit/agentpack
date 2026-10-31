# Gateway design system

The macOS kit for the gateway UI. Values live in `ui/src/index.css`; components live in `ui/src/components/kit/` (composed from the shadcn primitives in `ui/src/components/ui/`). Routes and screens are in `AGENTS.md` (Gateway UI) and `ui/SURFACE.md`.

## Tokens

`ui/src/index.css` holds the legacy kit tokens unchanged: `--font`, `--mono`, `--fs*`, `--bar-h`, `--status-h`, `--ctl-h`, `--row-h*`, `--s1…7`, `--r-*` and all colors. Components use only these tokens. The kit adds the following. The dark-mode values override the light ones where noted:

```css
:root {
  --tab-h: 49px;              /* phone tab bar */
  --hud: oklch(.2 .004 260 / 92%);
  --hud-fg: oklch(.98 0 0);
  --ok-word: oklch(.42 .13 150);
  --warn-word: oklch(.47 .11 70);
  --danger-word: oklch(.5 .2 27);
}
/* in both dark blocks */
--hud: oklch(.32 .002 260 / 94%);
--ok-word: oklch(.78 .15 150);
--warn-word: oklch(.83 .13 75);
--danger-word: oklch(.76 .16 25);
```

shadcn tokens map onto the kit tokens in the same file:

```css
:root {
  --background: var(--window); --foreground: var(--fg);
  --card: var(--window); --card-foreground: var(--fg);
  --popover: var(--chrome); --popover-foreground: var(--fg);
  --primary: var(--accent); --primary-foreground: var(--accent-fg);
  --secondary: var(--control); --secondary-foreground: var(--fg);
  --muted: var(--track); --muted-foreground: var(--fg2);
  --accent-ui: var(--accent-soft);
  --destructive: var(--danger);
  --border: var(--sep); --input: var(--sep); --ring: var(--accent);
  --radius: var(--r-ctl);
}
```

The kit's `--accent` token and shadcn's `--accent` collide. In `@theme inline`, `--color-accent` points at `--accent-ui` and `--color-primary` points at `--primary` (which is `--accent`). Do not rename the kit tokens.

## Metrics

| Element | Desktop | At ≤ 760 px |
|---|---|---|
| Toolbar | 40 px | 44 px |
| Status bar | 24 px | hidden, replaced by 49 px tab bar |
| Text | 13 / 12 / 11 px | same |
| Control | 24 px, small 20 px | 28 px |
| Row | 26 px, two-line 38 px | 32 px, two-line 44 px |
| Group header | 22 px | 24 px |
| Sidebar | 264 px, 220–420 px | full pane |
| List pane | 300 px | full pane |
| Radius | control and row 5, popover 8, sheet 10 | same |
| Tooltip delay | 700 ms | none |

Separators are 0.5 px. Panes scroll on their own. There is no page scroll, no page heading and no marketing padding. Only values from these tokens may appear in components.

## Kit components

Components live in `ui/src/components/kit/`.

| Component | Built on | Rules |
|---|---|---|
| `AppWindow` | CSS grid | Rows: toolbar, content, status bar. Nothing else is a grid child |
| `Toolbar`, `ToolbarButton` | `Button` | Borderless with hover fill. Leading order: sidebar toggle, view switcher, title. Trailing: view tools, gear |
| `StatusBar`, `StatusItem` | plain | 24 px high on desktop, hidden at ≤ 760 px (see Metrics) |
| `SourceList`, `SourceRow`, `GroupHeader` | plain + roving tabindex | Row is `<Link>` or button. Selected is gray when the list is unfocused and accent when focused. Trailing `⋯` visible on hover or selection |
| `SegmentedControl` | `ToggleGroup` | 24 px track, 20 px thumb |
| `SearchField` | `Input` | Track fill, icon inset, `⌘F` focuses it |
| `Field`, `FieldGrid` | `Input`, `Select` | 120 px right-aligned labels, 8 px row gap. One column on phone |
| `Sheet` | `Dialog` | Drops from the toolbar. Title, body, then buttons bottom-right: Cancel, primary. Bottom sheet on phone. No close `✕` |
| `ConfirmAlert` | `AlertDialog` | 280 px, centered. Title names the object. Stacked buttons, destructive first, Cancel focused |
| `Menu`, `RowContextMenu` | `DropdownMenu`, `ContextMenu` | 22 px items, icon + label + shortcut |
| `PopupSelect` | `Select` | Replaces every native `<select>` |
| `Banner` | plain | Inside a pane, top of its scroller. Icon, one sentence, optional action. Tones: info, warn, danger |
| `Hud` | `sonner` | Above the status bar, centered, `--hud` fill, 4 s |
| `StatusDot` | plain | See state vocabulary. Always with a word or `aria-label` plus tooltip |
| `Inspector`, `InspectorHead`, `KeyValue`, `Section` | plain | Max width 1180 px, two columns at ≥ 1260 px |
| `DataGrid` | `<table>` | Sticky 22 px header, 26 px rows, fixed layout, one flexible column |
| `Meter` | `<meter>` | 6 px, accent fill. Warn at ≤ 20% left, danger at ≤ 5%. No meter when unknown |
| `Hash` | `<code>` | First 7 characters, full value in tooltip, click copies, Hud says `Copied` |
| `Empty`, `RowSkeleton`, `Spinner` | plain | Empty says what appears and how. Spinner respects reduced motion |
| `Kbd` | plain | 11 px, muted |

## State vocabulary

Use these exact words everywhere.

| State | Glyph | Word | On selected row |
|---|---|---|---|
| Running | filled green dot | Running | white dot |
| Stopped | hollow ring | Stopped | white ring |
| Restarting | spinner | Restarting | white spinner |
| Offline | dashed ring | Offline | white dashed ring |
| Read-only | filled gray dot | Read-only | white dot |
| Needs update | filled amber dot | Needs update | white dot |

## Alert model

Each situation has one surface. This replaces `notice()`, `terminalNotice()`, `*-status` lines and `window.confirm`.

| Situation | Surface | Example copy |
|---|---|---|
| An action finished | `Hud`, 4 s | `Agent stopped` |
| An action failed and nothing is open | `Hud`, stays until dismissed, with `Retry` | `Couldn't stop claude on oracle-vm: worker failed` |
| Ongoing state of a pane | `Banner` | `mac-studio is offline. Showing its last known sessions` |
| Ongoing state of the terminal | `Banner` in the terminal column | `Connection lost. Reconnecting…` |
| Irreversible or disruptive action | `ConfirmAlert` | Title `Stop claude in agentpack?` Body `On oracle-vm. Its current conversation may end.` Buttons `Stop agent`, `Cancel` |
| Form validation or server rejection | Inline under the field | `Enter a name.` |

Errors say what failed and what to do next. Do not write `Error:`, `Oops` or apologies.
