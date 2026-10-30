import { createContext, type ReactNode, type RefObject, useContext, useState } from "react"
import { type PanelImperativeHandle, usePanelRef } from "react-resizable-panels"

type Sidebar = {
  panelRef: RefObject<PanelImperativeHandle | null>
  collapsed: boolean
  setCollapsed: (collapsed: boolean) => void
  toggle: () => void
}

const SidebarContext = createContext<Sidebar | null>(null)

// One sidebar state for every list-detail view, so the toggle means the same thing everywhere.
export function SidebarProvider({ children }: { children: ReactNode }) {
  const panelRef = usePanelRef()
  const [collapsed, setCollapsed] = useState(false)
  const toggle = () => {
    const panel = panelRef.current
    if (!panel) return
    if (panel.isCollapsed()) panel.expand()
    else panel.collapse()
  }
  return (
    <SidebarContext value={{ panelRef, collapsed, setCollapsed, toggle }}>
      {children}
    </SidebarContext>
  )
}

export function useSidebar() {
  const sidebar = useContext(SidebarContext)
  if (!sidebar) throw new Error("useSidebar must be used inside SidebarProvider")
  return sidebar
}
