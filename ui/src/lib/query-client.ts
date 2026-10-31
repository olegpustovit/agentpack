import { QueryClient } from "@tanstack/react-query"
import { ApiError } from "./api"

export function createQueryClient() {
  return new QueryClient({
    defaultOptions: {
      queries: {
        staleTime: 30 * 1000,
        gcTime: 60 * 60 * 1000,
        refetchOnWindowFocus: true,
        retry: (count, error) => !(error instanceof ApiError && error.status < 500) && count < 2,
        // Browser tests assert what happens after retries, and backoff would be most of their runtime.
        retryDelay: navigator.webdriver ? 0 : undefined,
      },
    },
  })
}
