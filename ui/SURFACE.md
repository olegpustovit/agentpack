# Gateway workspace

Operate mode. Users connect to coding sessions, inspect saved package configurations, and confirm installs across trusted projects. The approved reference is [artifact version 9](https://artifacts.microapps.space/a/a88mxr1gd37z), preserved in `docs/gateway-workspace-plan.md`. Real gateway data replaces the prototype fixtures.

## Direction contract

THESIS: A terminal-led desktop workspace with compact source lists and explicit install/restore review.

OWN-WORLD: The reference's system-font typography, light/dark surfaces, small native-style controls, subtle separators, and restrained blue selection. Status combines a word with a dot/ring. No external fonts or CDN assets.

STORY: Select a session, inspect its live host grid, type alongside the local terminal, then inspect saved configurations and choose destinations before changing them. Offline work remains a reviewable draft.

FIRST VIEWPORT: A 40px toolbar, resizable left Sessions list (220–420px), edge-to-edge terminal, and 24px status bar. Library uses a 300px list and inspector; System has one sidebar (Usage, Limits, settings by scope, machines). Machine pages show a package matrix; Usage is one hero metric, a scrubbable bar chart and a sortable sessions grid, filtered from a toolbar popover by project, machine and agent; its Limits pane is a compact provider list with automatic selection of a machine reporting usable windows. Each window shows remaining and used percentages and its reset time; plan, source machine, observation time, and expandable reports from other machines preserve provenance. Unknown values have no meter. At 390px, panes switch and dialogs become bottom sheets. Work includes recent
closed chats and links to a date-grouped, searchable History view. The project
page stays beside the Sessions sidebar on desktop and becomes a full-screen
detail on phones. Its search covers installed items, the Library and the
catalog, with installs and server switches applied in place.

FORM: User-pinned reference implementation; code-led reproduction of the supplied HTML/CSS/behavior. No direction roll applies: the user's explicit 1:1 reference fixes the design.

FINISH: unreviewed and undocumented is unfinished; this build ends with the finish review, the verdict, DESIGN.md, and every shipping raster carrying its provenance

## Evidence

`tests/gateway.spec.ts` exercises the behavioral contract. `tests/review-captures.spec.ts` captures both themes and desktop/mobile states. The optional host-color query and screen-snapshot protocol are outside the chosen scope; the UI keeps the last frame dimmed through reconnect.

`public/icon.svg` comes from the approved artifact. `public/icon-192.png` and `public/icon-512.png` are raster exports of that SVG for Chromium installability; they contain no generated artwork.
