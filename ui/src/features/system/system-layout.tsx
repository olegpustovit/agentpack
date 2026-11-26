import type { ReactNode } from "react"
import { ListDetail } from "@/features/shell/list-detail"
import { SystemSidebar } from "./system-sidebar"

// Usage, machines and settings beside one sidebar.
export default function SystemLayout({ children }: { children: ReactNode }) {
  return <ListDetail list={<SystemSidebar />}>{children}</ListDetail>
}
