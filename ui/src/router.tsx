import { createRouter } from "@tanstack/react-router"
import { Banner } from "./components/kit/feedback"
import { Button } from "./components/ui/button"
import { routeTree } from "./routeTree.gen"
import { createQueryClient } from "./lib/query-client"

export const queryClient = createQueryClient()

export const router = createRouter({
  routeTree,
  context: { queryClient },
  defaultPreload: "intent",
  defaultPreloadStaleTime: 0,
  defaultErrorComponent: ({ error, reset }) => (
    <Banner tone="danger" action={<Button variant="ghost" onClick={reset}>Retry</Button>}>
      {`This view couldn't be shown: ${(error as Error).message}`}
    </Banner>
  ),
})

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router
  }
}
