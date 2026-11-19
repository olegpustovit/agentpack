import { createFileRoute, Outlet } from "@tanstack/react-router"
import { looseSearch } from "@/lib/search"
import { LibraryList } from "@/features/library/library-list"
import { ListDetail } from "@/features/shell/list-detail"

export const Route = createFileRoute("/_app/library")({
  // Loose, so the child routes' own search keys pass through this layout.
  validateSearch: looseSearch({
    q: "text",
    type: "text",
    sort: "text",
    // Trigger keys are flags: the search parser JSON-decodes "1" to a number.
    add: "flag",
  }),
  component: () => (
    <ListDetail list={<LibraryList />}>
      <Outlet />
    </ListDetail>
  ),
})
