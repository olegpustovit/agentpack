import { createFileRoute, Outlet } from "@tanstack/react-router"
import { z } from "zod"
import { ListDetail } from "@/features/shell/list-detail"
import { SessionList } from "@/features/sessions/session-list"

export const Route = createFileRoute("/_app/sessions")({
  // Loose, so the child routes' own search keys (q, group, new) pass through this layout.
  validateSearch: z.looseObject({
    q: z.string().optional().catch(undefined),
    group: z.string().optional().catch(undefined),
    new: z.coerce.string().optional().catch(undefined),
  }),
  component: () => (
    <ListDetail list={<SessionList />}>
      <Outlet />
    </ListDetail>
  ),
})
