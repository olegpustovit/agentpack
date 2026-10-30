import { createRouter } from "@tanstack/react-router"
import { routeTree } from "./routeTree.gen"
import { createQueryClient } from "./lib/query-client"

export const queryClient = createQueryClient()

export const router = createRouter({
  routeTree,
  context: { queryClient },
  defaultPreload: "intent",
  defaultPreloadStaleTime: 0,
})

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router
  }
}
