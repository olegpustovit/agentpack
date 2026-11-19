import { useEffect, type CSSProperties } from "react"
import { X } from "lucide-react"
import { Toaster as Sonner, toast, type ToasterProps } from "sonner"
import { hudListening } from "@/lib/hud"

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

// The Hud surface: centered above the status bar, 4 s, with a close ✕. Loaded with the first toast.
export default function HudToaster() {
  // Runs after Sonner's own subscription (a child's effects run first), so queued toasts are seen.
  useEffect(() => hudListening(toast), [])
  return (
    <Toaster
      position="bottom-center"
      duration={4000}
      offset="calc(var(--status-h) + var(--s4))"
      closeButton
      icons={{ close: <X className="size-(--fs)" aria-hidden /> }}
    />
  )
}
