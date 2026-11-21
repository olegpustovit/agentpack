import { createContext, type ReactNode, useContext, useState } from "react"
import { createPortal } from "react-dom"
import { StatusBar as StatusBarFrame, StatusItem } from "@/components/kit/window"
import { ActivityItem } from "./activity"
import { ConnectionItem } from "./connection"

type SlotTarget = {
  element: HTMLElement | null
  setElement: (element: HTMLElement | null) => void
  terminal: HTMLElement | null
  setTerminal: (element: HTMLElement | null) => void
}

const SlotContext = createContext<SlotTarget | null>(null)

// Provided by the app shell, above both the status bar and the routed content.
export function StatusProvider({ children }: { children: ReactNode }) {
  const [element, setElement] = useState<HTMLElement | null>(null)
  const [terminal, setTerminal] = useState<HTMLElement | null>(null)
  return <SlotContext value={{ element, setElement, terminal, setTerminal }}>{children}</SlotContext>
}

export function StatusBar() {
  const target = useContext(SlotContext)
  return (
    <StatusBarFrame>
      <ConnectionItem />
      <StatusItem ref={target?.setElement} className="flex-1 border-(--sep) not-empty:border-l-[0.5px] not-empty:ps-(--s4)" />
      <ActivityItem />
      <StatusItem ref={target?.setTerminal} className="empty:hidden border-l-[0.5px] border-(--sep) ps-(--s4)" />
    </StatusBarFrame>
  )
}

// Routes render their status text here; it appears only while that route is mounted.
export function StatusSlot({ children, terminal = false }: { children: ReactNode; terminal?: boolean }) {
  const target = useContext(SlotContext)
  const element = terminal ? target?.terminal : target?.element
  return element ? createPortal(children, element) : null
}
