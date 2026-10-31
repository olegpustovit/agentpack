import { createContext, type ReactNode, useContext, useState } from "react"
import { createPortal } from "react-dom"

type SlotElement = HTMLElement | null
type SlotTarget = { element: SlotElement; setElement: (element: SlotElement) => void; title: SlotElement; setTitle: (element: SlotElement) => void }

const ToolbarSlotContext = createContext<SlotTarget | null>(null)

// Provided by the app shell, above the toolbar and the routed content.
export function ToolbarProvider({ children }: { children: ReactNode }) {
  const [element, setElement] = useState<SlotElement>(null)
  const [title, setTitle] = useState<SlotElement>(null)
  return <ToolbarSlotContext value={{ element, setElement, title, setTitle }}>{children}</ToolbarSlotContext>
}

// Sits before the gear. Empty targets take no space, so routes without a slot see no gap.
export function ToolbarTarget() {
  const target = useContext(ToolbarSlotContext)
  return <div ref={target?.setElement} className="flex shrink-0 items-center gap-(--s2) empty:hidden" />
}

// Routes render trailing toolbar controls here; they appear only while that route is mounted.
export function ToolbarSlot({ children }: { children: ReactNode }) {
  const target = useContext(ToolbarSlotContext)
  return target?.element ? createPortal(children, target.element) : null
}

// The empty target is a CSS peer: the shell's fallback view label shows only while no route fills it.
export function ToolbarTitleTarget() {
  const target = useContext(ToolbarSlotContext)
  return <div ref={target?.setTitle} className="peer min-w-0 empty:hidden" />
}

// Object title and subtitle. Routes render this instead of a page heading, which would eat pane height.
export function ToolbarTitle({ title, subtitle }: { title: string; subtitle?: string }) {
  const target = useContext(ToolbarSlotContext)
  if (!target?.title) return null
  return createPortal(
    <div className="flex min-w-0 flex-col justify-center leading-tight">
      <span className="truncate text-(length:--fs) font-semibold">{title}</span>
      {subtitle && <span className="truncate text-(length:--fs-xs) text-(--fg2)">{subtitle}</span>}
    </div>,
    target.title,
  )
}
