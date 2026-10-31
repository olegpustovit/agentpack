---
name: "Agentpack gateway"
description: "A compact, terminal-led workspace for managing Agentpack sessions and package installs."
colors:
  window-light: "oklch(.992 .001 260)"
  chrome-light: "oklch(.952 .003 260)"
  sidebar-light: "oklch(.968 .003 260)"
  control-light: "oklch(1 0 0)"
  track-light: "oklch(.915 .004 260)"
  foreground-light: "oklch(.2 .004 260)"
  secondary-foreground-light: "oklch(.47 .006 260)"
  separator-light: "oklch(.885 .003 260)"
  accent-light: "oklch(.57 .19 255)"
  window-dark: "oklch(.2 0 0)"
  chrome-dark: "oklch(.255 .002 260)"
  sidebar-dark: "oklch(.225 .002 260)"
  control-dark: "oklch(.43 .002 260)"
  track-dark: "oklch(.3 .002 260)"
  foreground-dark: "oklch(.96 0 0)"
  secondary-foreground-dark: "oklch(.74 .004 260)"
  separator-dark: "oklch(1 0 0 / 11%)"
  accent-dark: "oklch(.64 .17 255)"
  terminal-background: "#171b22"
  terminal-foreground: "#e6e8ed"
typography:
  body:
    fontFamily: '-apple-system, BlinkMacSystemFont, "SF Pro Text", "Helvetica Neue", system-ui, sans-serif'
    fontSize: "13px"
    fontWeight: 400
    lineHeight: 1.38
    letterSpacing: "-.006em"
  small:
    fontFamily: '-apple-system, BlinkMacSystemFont, "SF Pro Text", "Helvetica Neue", system-ui, sans-serif'
    fontSize: "12px"
    fontWeight: 400
    lineHeight: 1.38
  meta:
    fontFamily: '-apple-system, BlinkMacSystemFont, "SF Pro Text", "Helvetica Neue", system-ui, sans-serif'
    fontSize: "11px"
    fontWeight: 400
    lineHeight: 1.38
  inspector-title:
    fontFamily: '-apple-system, BlinkMacSystemFont, "SF Pro Text", "Helvetica Neue", system-ui, sans-serif'
    fontSize: "15px"
    fontWeight: 600
    lineHeight: 1.25
  code:
    fontFamily: "ui-monospace, SFMono-Regular, Menlo, Consolas, monospace"
    fontSize: "11px"
    fontWeight: 400
    lineHeight: 1.5
rounded:
  control: "5px"
  row: "5px"
  popover: "8px"
  sheet: "10px"
spacing:
  1: "2px"
  2: "4px"
  3: "6px"
  4: "8px"
  5: "12px"
  6: "16px"
  7: "20px"
components:
  button-primary:
    backgroundColor: "{colors.accent-light}"
    textColor: "#ffffff"
    rounded: "{rounded.control}"
    height: "24px"
  button-neutral:
    backgroundColor: "{colors.control-light}"
    textColor: "{colors.foreground-light}"
    rounded: "{rounded.control}"
    height: "24px"
  input:
    backgroundColor: "{colors.control-light}"
    textColor: "{colors.foreground-light}"
    rounded: "{rounded.control}"
    height: "24px"
  source-row-selected:
    backgroundColor: "{colors.accent-light}"
    textColor: "#ffffff"
    rounded: "{rounded.row}"
    height: "26px"
  chip:
    backgroundColor: "{colors.track-light}"
    textColor: "{colors.secondary-foreground-light}"
    rounded: "11px"
    height: "22px"
---

# Design System: Agentpack gateway

## Overview

**Creative North Star: "The Terminal-Led Native Workspace"**

The gateway follows the approved compact desktop reference: a quiet utility window organized around the host terminal, with source lists, inspectors, and install review kept close at hand. System typography, small controls, restrained blue selection, and subtle separators keep the interface familiar and information-dense without making it feel like a web dashboard.

Light and dark appearances share the same hierarchy. Terminal defaults and application chrome adapt to the chosen appearance; explicit colors emitted by the host remain intact. On narrow screens, panes switch instead of compressing into unreadable columns, and install sheets rise from the bottom.

**Key Characteristics:**
- Terminal-led workspace with compact, native-style chrome.
- Selection uses restrained blue; status always pairs a word with a dot or ring.
- Terminal defaults follow application appearance; host-emitted explicit colors remain intact.

## Colors

The interface uses cool, near-neutral surfaces in both appearances, with blue reserved for selection, links, and primary actions; status colors remain distinct and labeled.

### Primary
- **Restrained workspace blue** (light and dark values are tokens): selection, links, focused actions, and active navigation.

### Neutral
- **Cool window, chrome, and sidebar surfaces:** separate the working canvas, toolbar/status chrome, and source-list regions in light and dark appearances.
- **Foreground and secondary foreground:** carry primary content and supporting metadata with clear contrast.
- **Hairline separators:** divide panes and sections without heavy framing.
- **Host terminal ink:** terminal default background and text follow application appearance.
- **Status colors:** green, amber, red, and neutral gray distinguish success, warning, danger, and idle; each state also has a word or shape.

**The Labeled State Rule.** Never communicate status by color alone; pair the colored dot or ring with a readable state label.

## Typography

**Display Font:** none; the interface uses system UI typography.
**Body Font:** system sans-serif (`-apple-system`, `BlinkMacSystemFont`, SF Pro Text, Helvetica Neue, system-ui).
**Label/Mono Font:** system monospace (ui-monospace, SFMono-Regular, Menlo, Consolas).

**Character:** Compact and familiar, with restrained weight changes rather than a separate display face. Monospace is reserved for terminal-adjacent values, code, and technical identifiers.

### Hierarchy
- **Body** (400, 13px, 1.38): default interface copy and controls.
- **Small** (400, 12px): secondary forms and supporting content.
- **Meta** (400, 11px): captions, subtitles, and status-bar labels.
- **Inspector title** (600, 15px, 1.25): selected object titles.
- **Code** (400, 11px, 1.5): compact technical values and code blocks.

## Layout

Desktop uses a 40px toolbar, independently scrolling panes, an edge-to-edge terminal, and a 24px status bar. The sessions source list is resizable around a 264px starting width; library and machine views use a roughly 300px list beside an inspector or matrix. A 1px pane splitter and half-pixel separators keep boundaries light. The inspector stays readable and expands to two columns only on wide windows (1260px and above).

At widths up to 760px, the toolbar grows to 44px and multi-pane views switch between list and detail. The terminal and sessions also switch panes. Sheets become bottom sheets, rows grow to 32px, and the status bar keeps the selected session visible. Spacing follows the 2, 4, 6, 8, 12, 16, and 20px rhythm.

## Elevation & Depth

Depth combines tonal surface shifts, fine inset strokes, and restrained control or popover shadows. Pane separation primarily comes from surface contrast and hairlines; floating menus and sheets receive the stronger shadow. Terminal depth remains owned by the host terminal surface.

### Shadow Vocabulary
- **Control edge:** a subtle half-pixel outline and low shadow for native-style buttons.
- **Popover and sheet:** broader shadow to separate transient surfaces from the workspace.

## Shapes

Controls and rows use gently rounded 5px corners. Popovers and code blocks use 8px corners; sheets use 10px. Chips are pill-shaped. Pane boundaries remain square and precise, with half-pixel separators rather than enclosing cards.

## Components

### Buttons
- **Shape:** compact rounded controls (5px).
- **Primary:** restrained blue fill, white text, 24px high on desktop and 28px on narrow screens.
- **Neutral:** appearance-aware control surface with a subtle edge shadow.
- **Toolbar:** borderless with hover fill; content actions use bordered controls. The primary action is the single filled action in a group.
- **Focus:** visible accent outline; disabled controls reduce opacity.

### Inputs / Fields
- **Style:** appearance-aware field surface, 5px corners, subtle inset stroke, 24px desktop height.
- **Focus:** accent border with a soft accent halo.

### Chips
- **Style:** track surface, secondary text, compact pill geometry (22px high).
- **State:** accent and warning variants use tinted surfaces and matching text.

### Cards / Containers
- The interface favors open panes and hairline dividers over card stacks. Callouts and code blocks use a softly inset outline and sidebar-toned surface.

### Navigation
- **Style:** compact segmented view switcher and source-list rows.
- **Selected:** inactive lists show a quiet neutral selection; keyboard-focused lists use blue with white text. Hover uses a subtle surface tint.
- **Mobile:** pane switching replaces side-by-side navigation and detail.

### Status Indicators
- A small green, amber, or neutral dot accompanies a state word; offline/idle uses a hollow ring. Status colors never carry meaning alone.

## Do's and Don'ts

### Do:
- **Do** preserve the 13px system-font body and compact 2–20px spacing rhythm.
- **Do** use hairline separators and quiet surface changes to organize panes.
- **Do** apply the chosen appearance to terminal defaults while preserving explicit host colors.
- **Do** switch panes on narrow screens and allow sheets to become bottom sheets.
- **Do** pair every status color with text or a distinct shape.

### Don't:
- **Don't** add an external display font or CDN-loaded visual asset.
- **Don't** use color alone to communicate state.
- **Don't** turn the terminal-led workspace into a page-scroll dashboard or add persistent page headers and footers.

### Provider limits

Limits uses a single 960px-wide list of providers with hairline dividers and the existing system typography. Each provider leads with the connected machine reporting usable windows, followed by plan and observation time. Remaining meters align with used/remaining text and reset details; unknown percentages have no meter. Other machine reports and connection failures use native disclosure controls. The 390px layout places meters below their labels and preserves source metadata. Refresh keeps the current reports visible while new observations arrive.

### Loading and discovery

Loading copy names the operation and uses the existing small spinner, with reduced-motion support and accessible status text. Limits shows each connected machine's pending/completed check and renders completed reports while slower nodes continue. Library and Machines keep cached content visible with an inline progress line. Failures replace loading copy with a useful error or retry action; they do not become empty states. Initial connection has a centered status instead of a blank window.
