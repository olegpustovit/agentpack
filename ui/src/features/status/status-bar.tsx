import { createContext, type ReactNode, useContext, useState } from "react"
import { createPortal } from "react-dom"
import { StatusBar as StatusBarFrame, StatusItem } from "@/components/kit/window"
import { ActivityItem } from "./activity"
import { ConnectionItem } from "./connection"

type SlotTarget = { element: HTMLElement | null; setElement: (element: HTMLElement | null) => void }

const SlotContext = createContext<SlotTarget | null>(null)

// Provided by the app shell, above both the status bar and the routed content.
export function StatusProvider({ children }: { children: ReactNode }) {
  const [element, setElement] = useState<HTMLElement | null>(null)
  return <SlotContext value={{ element, setElement }}>{children}</SlotContext>
}

export function StatusBar() {
  const target = useContext(SlotContext)
  return (
    <StatusBarFrame>
      <ConnectionItem />
      <StatusItem ref={target?.setElement} className="flex-1 border-(--sep) not-empty:border-l-[0.5px] not-empty:ps-(--s4)" />
      <ActivityItem />
    </StatusBarFrame>
  )
}

// Routes render their status text here; it appears only while that route is mounted.
export function StatusSlot({ children }: { children: ReactNode }) {
  const target = useContext(SlotContext)
  return target?.element ? createPortal(children, target.element) : null
}
