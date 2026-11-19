import type { ThreadItem, ToolDiffBlock, ToolPayload } from "./model"

export type DiffChange = {
  path: string
  add: number
  del: number
  toolId?: string
  turn: number
}

function lineCount(text: string | undefined): number {
  if (!text) return 0
  const lines = text.split("\n")
  if (lines.length > 1 && lines.at(-1) === "") lines.pop()
  return lines.length
}

// Turn numbering: a turn starts at every reduced item of kind "prompt" or "user_message_chunk" (1-based);
// a tool item belongs to the latest turn. Mirrors revertPlan in internal/network/conversation_revert.go.
export function listDiffChanges(items: ThreadItem[]): DiffChange[] {
  const out: DiffChange[] = []
  let turn = 0
  for (const item of items) {
    if (item.kind === "prompt" || item.kind === "user_message_chunk") turn++
    if (item.kind !== "tool") continue
    const tool = item.data as ToolPayload
    for (const block of tool.content ?? []) {
      if ((block as ToolDiffBlock).type !== "diff") continue
      const diff = block as ToolDiffBlock
      const path = diff.path?.trim() || "change"
      out.push({ path, add: lineCount(diff.newText), del: lineCount(diff.oldText), toolId: tool.toolCallId, turn })
    }
  }
  return out
}

// Unique per edit: the same file edited twice must not share a DOM id.
export function diffAnchorId(path: string, toolId?: string) {
  return `diff-${toolId ? `${encodeURIComponent(toolId)}-` : ""}${encodeURIComponent(path)}`
}

// One row per file, summed; the anchor is the file's latest edit. Pass one turn's changes or all.
export function groupByPath(changes: DiffChange[]): (DiffChange & { anchor: string })[] {
  const byPath = new Map<string, DiffChange & { anchor: string }>()
  for (const change of changes) {
    const prev = byPath.get(change.path)
    byPath.set(change.path, {
      ...change,
      add: (prev?.add ?? 0) + change.add,
      del: (prev?.del ?? 0) + change.del,
      anchor: diffAnchorId(change.path, change.toolId),
    })
  }
  return [...byPath.values()]
}

// A changed file as the inspector lists it: its name, and its folder relative to the project.
export function fileLabel(path: string, project?: string): { name: string; folder: string } {
  const root = project?.replace(/\/+$/, "")
  const relative = root && path.startsWith(root + "/") ? path.slice(root.length + 1) : path
  const cut = relative.lastIndexOf("/")
  return cut < 0 ? { name: relative, folder: "" } : { name: relative.slice(cut + 1), folder: relative.slice(0, cut) }
}
