import { queryOptions } from "@tanstack/react-query"
import { z } from "zod"
import { api } from "@/lib/api"
import type { ProjectRow } from "@/lib/api-types"

const projectRowSchema = z.object({
  directory: z.string(),
  name: z.string(),
  folder_id: z.string(),
  available: z.boolean(),
  unavailable_reason: z.string(),
  last_used: z.string(),
  // Go sends null for nil slices.
  launches: z
    .array(z.object({ harness: z.string(), mode: z.string(), last_used: z.string() }))
    .nullish()
    .transform((v) => v ?? []),
  packages: z
    .array(
      z.object({
        module: z.string(),
        commit: z.string(),
        content_hash: z.string(),
        cache_key: z.string(),
        kind: z.string(),
        name: z.string(),
        url: z.string(),
        direct: z.boolean(),
      }),
    )
    .nullish()
    .transform((v) => v ?? []),
  mcp: z
    .array(
      z.object({
        name: z.string(),
        source: z.string(),
        launcher: z.string(),
        status: z.string(),
        package: z.string(),
        version: z.string(),
        integrity: z.string(),
        host: z.string(),
        reason: z.string(),
      }),
    )
    .nullish()
    .transform((v) => v ?? []),
  error: z.string(),
})

export const projectsQuery = (machine: string) =>
  queryOptions({
    queryKey: ["m", machine, "projects"],
    queryFn: async (): Promise<ProjectRow[]> =>
      z.array(projectRowSchema).parse(await api<unknown>("/api/projects", { machine })),
    staleTime: 5 * 60 * 1000,
    refetchInterval: 60 * 1000,
  })
