import { StrictMode } from "react"
import { createRoot } from "react-dom/client"
import { QueryClientProvider } from "@tanstack/react-query"
import { RouterProvider } from "@tanstack/react-router"
import { setUnauthorizedHandler } from "./lib/api"
import { queryClient, router } from "./router"

setUnauthorizedHandler(() => {
  queryClient.clear()
  void router.navigate({ to: "/login" })
})

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  </StrictMode>,
)
