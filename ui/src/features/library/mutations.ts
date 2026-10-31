import { api } from "@/lib/api"
import type { Agentpack, Draft, PackagePin, Revision } from "@/lib/api-types"

// Shapes from internal/network/library*.go and internal/registry/drafts.go.
export type PackageDiff = { add: string[]; update: string[]; remove: string[]; mode?: string }

export type DraftReview = {
  draft: Draft
  ready: boolean
  pins_match: boolean
  folder_match: boolean
  reason: string
  diff?: PackageDiff
}

export type DraftInput = {
  item: string
  revision?: string
  node: string
  directory: string
  reason: string
  pack_id?: string
  folder_identity: string
  expected_pins: Record<string, PackagePin>
}

export type SaveRevisionInput = {
  directory: string
  name: string
  agentpack_id?: string
  label?: string
  mode: string
  auto_capture: boolean
}

// `machine` is the gateway id ("local" for this gateway). The api helper omits `node` for it.
export function installPackage(machine: string, directory: string, spec: string, restart: boolean) {
  return api<{ installed: boolean }>("/api/packages", { machine, body: { directory, spec, restart } })
}

export function restorePreview(machine: string, packId: string, revisionId: string, directory: string) {
  return api<PackageDiff>(
    `/api/library/agentpacks/${encodeURIComponent(packId)}/revisions/${encodeURIComponent(revisionId)}/restore-preview?directory=${encodeURIComponent(directory)}`,
    { machine },
  )
}

export function restoreRevision(machine: string, packId: string, revisionId: string, directory: string, restart: boolean) {
  return api<{ restored: boolean }>(
    `/api/library/agentpacks/${encodeURIComponent(packId)}/revisions/${encodeURIComponent(revisionId)}/restore`,
    { machine, body: { directory, restart } },
  )
}

export function saveRevision(machine: string, input: SaveRevisionInput) {
  return api<{ agentpack: Agentpack; revision: Revision }>("/api/library/agentpacks", { machine, body: input })
}

export function renameAgentpack(machine: string, id: string, name: string) {
  return api<Agentpack>(`/api/library/agentpacks/${encodeURIComponent(id)}`, { machine, method: "PATCH", body: { name } })
}

export function saveDraft(input: DraftInput) {
  return api<Draft>("/api/library/drafts", { body: input })
}

export function reviewDraft(id: string) {
  return api<DraftReview>(`/api/library/drafts/${encodeURIComponent(id)}/review`, { body: {} })
}

export function discardDraft(id: string) {
  return api<{ discarded: boolean }>(`/api/library/drafts/${encodeURIComponent(id)}`, { method: "DELETE" })
}

// Applies a draft through the normal install or restore endpoint, then discards it.
export async function applyDraft(draft: Draft, restart: boolean) {
  if (draft.pack_id) {
    if (!draft.revision) throw new Error("This pending restore has no revision. Discard it and save again.")
    await restoreRevision(draft.node, draft.pack_id, draft.revision, draft.directory, restart)
  } else {
    await installPackage(draft.node, draft.directory, draft.item, restart)
  }
  await discardDraft(draft.id)
}

export function errorText(error: unknown): string {
  return error instanceof Error ? error.message : "the request did not complete"
}
