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

// The Hud surface: top right under the toolbar, like a desktop notification, so it never covers the
// composer. 4 s, with a close ✕. Loaded with the first toast.
export default function HudToaster() {
  // Runs after Sonner's own subscription (a child's effects run first), so queued toasts are seen.
  useEffect(() => hudListening(toast), [])
  return (
    <Toaster
      position="top-right"
      duration={4000}
      offset={{ top: "calc(var(--bar-h) + var(--s3))", right: "var(--s4)" }}
      mobileOffset={{ top: "calc(var(--bar-h) + var(--s3))" }}
      closeButton
      icons={{ close: <X className="size-(--fs)" aria-hidden /> }}
    />
  )
}
