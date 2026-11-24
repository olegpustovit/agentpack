import { createFileRoute, Outlet, useNavigate } from "@tanstack/react-router"
import { looseSearch } from "@/lib/search"
import { AddPackageSheet } from "@/features/library/add-package-sheet"
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
  component: LibraryLayout,
})

function LibraryLayout() {
  const { add } = Route.useSearch()
  const navigate = useNavigate()
  return (
    <>
      <ListDetail list={<LibraryList />}>
        <Outlet />
      </ListDetail>
      {add !== undefined && (
        <AddPackageSheet
          open
          initialSpec={add === "1" ? undefined : add}
          onOpenChange={(open) => {
            if (!open) void navigate({ to: ".", search: (prev) => ({ ...prev, add: undefined }), replace: true })
          }}
        />
      )}
    </>
  )
}
