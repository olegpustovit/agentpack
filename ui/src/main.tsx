import "./index.css"
import { StrictMode } from "react"
import { createRoot } from "react-dom/client"
import { QueryClientProvider } from "@tanstack/react-query"
import { RouterProvider } from "@tanstack/react-router"
import { setUnauthorizedHandler } from "./lib/api"
import { terminalPool } from "./features/terminal/pool"
import { forgetLimits } from "./features/limits/queries"
import { queryClient, router } from "./router"
import { breadcrumb, installTelemetry } from "./lib/telemetry"
import { configQuery } from "./lib/config"

const entryBuildID = document.querySelector<HTMLScriptElement>('script[type="module"][src]')?.src.match(/index-(.+)\.js$/)?.[1]
installTelemetry(() => ({
  route: router.state.matches.at(-1)?.fullPath ?? "", search: router.state.location.search,
  ui_build_id: entryBuildID || queryClient.getQueryData(configQuery.queryKey)?.ui_build_id || "dev",
}))
router.subscribe("onResolved", () => breadcrumb({ kind: "navigate", name: router.state.matches.at(-1)?.fullPath ?? "" }))

// A 401 ends the session: close every terminal socket before the login screen takes over.
setUnauthorizedHandler(() => {
  terminalPool.disposeAll()
  queryClient.clear()
  forgetLimits()
  void router.navigate({ to: "/login" })
})

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  </StrictMode>,
)
