import type { CSSProperties } from "react"
import { Toaster as Sonner, type ToasterProps } from "sonner"

// shortcut: theme is read per render, not subscribed to data-theme changes, re-render Toaster from Settings when a live theme switch lands
const Toaster = (props: ToasterProps) => (
  <Sonner
    theme={(document.documentElement.dataset.theme as ToasterProps["theme"]) ?? "system"}
    style={
      {
        "--normal-bg": "var(--hud)",
        "--normal-text": "var(--hud-fg)",
        "--normal-border": "transparent",
        "--border-radius": "var(--r-ctl)",
      } as CSSProperties
    }
    {...props}
  />
)

export { Toaster }
