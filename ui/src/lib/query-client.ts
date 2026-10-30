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
      },
    },
  })
}
