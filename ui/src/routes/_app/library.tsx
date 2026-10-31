import { createFileRoute, Outlet } from "@tanstack/react-router"
import { z } from "zod"
import { LibraryList } from "@/features/library/library-list"
import { ListDetail } from "@/features/shell/list-detail"

export const Route = createFileRoute("/_app/library")({
  // Loose, so the child routes' own search keys pass through this layout.
  validateSearch: z.looseObject({
    q: z.string().optional().catch(undefined),
    type: z.string().optional().catch(undefined),
    sort: z.string().optional().catch(undefined),
  }),
  component: () => (
    <ListDetail list={<LibraryList />}>
      <Outlet />
    </ListDetail>
  ),
})
