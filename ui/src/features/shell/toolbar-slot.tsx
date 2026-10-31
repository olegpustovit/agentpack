import { createContext, type ReactNode, useContext, useState } from "react"
import { createPortal } from "react-dom"

type SlotTarget = { element: HTMLElement | null; setElement: (element: HTMLElement | null) => void }

const ToolbarSlotContext = createContext<SlotTarget | null>(null)

// Provided by the app shell, above the toolbar and the routed content.
export function ToolbarProvider({ children }: { children: ReactNode }) {
  const [element, setElement] = useState<HTMLElement | null>(null)
  return <ToolbarSlotContext value={{ element, setElement }}>{children}</ToolbarSlotContext>
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
