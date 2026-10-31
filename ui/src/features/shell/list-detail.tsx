import type { ReactNode } from "react"
import { useEffect } from "react"
import { useDefaultLayout } from "react-resizable-panels"
import { ResizableHandle, ResizablePanel, ResizablePanelGroup } from "@/components/ui/resizable"
import { useIsPhone } from "./use-is-phone"
import { useRoutePosition } from "./views"
import { useSidebar } from "./sidebar"

const sidebarWidth = 264

// On phones the route decides which pane shows; panels are not used there.
export function ListDetail({ list, children }: { list: ReactNode; children: ReactNode }) {
  return useIsPhone() ? <PhoneListDetail list={list}>{children}</PhoneListDetail> : <SidebarListDetail list={list}>{children}</SidebarListDetail>
}

function PhoneListDetail({ list, children }: { list: ReactNode; children: ReactNode }) {
  const { detail } = useRoutePosition()
  return <div className="flex h-full min-h-0 min-w-0 flex-col">{detail ? children : list}</div>
}

function SidebarListDetail({ list, children }: { list: ReactNode; children: ReactNode }) {
  const sidebar = useSidebar()
  const layout = useDefaultLayout({ id: "agentpack-sidebar", panelIds: ["sidebar", "content"] })
  const sync = () => sidebar.setCollapsed(sidebar.panelRef.current?.isCollapsed() ?? false)

  // A restored layout can start collapsed; the toggle must reflect that from the first render.
  useEffect(sync, [])

  return (
    <ResizablePanelGroup
      id="agentpack-sidebar"
      className="min-h-0"
      defaultLayout={layout.defaultLayout}
      onLayoutChanged={layout.onLayoutChanged}
    >
      <ResizablePanel
        id="sidebar"
        role="region"
        aria-label="Sidebar"
        panelRef={sidebar.panelRef}
        defaultSize={sidebarWidth}
        minSize={220}
        maxSize={420}
        collapsible
        collapsedSize={0}
        onResize={sync}
        className="min-w-0 bg-(--sidebar)"
      >
        {list}
      </ResizablePanel>
      <ResizableHandle
        className="w-0 cursor-col-resize border-l-[0.5px] border-(--sep) bg-transparent after:w-[7px]"
        onDoubleClick={() => sidebar.panelRef.current?.resize(sidebarWidth)}
      />
      <ResizablePanel id="content" className="min-w-0">
        {children}
      </ResizablePanel>
    </ResizablePanelGroup>
  )
}
