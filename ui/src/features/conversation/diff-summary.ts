import type { ThreadItem, ToolDiffBlock, ToolPayload } from "./model"

export type DiffChange = {
  path: string
  add: number
  del: number
}

function lineCount(text: string | undefined): number {
  if (!text) return 0
  const lines = text.split("\n")
  if (lines.length > 1 && lines.at(-1) === "") lines.pop()
  return lines.length
}

export function listDiffChanges(items: ThreadItem[]): DiffChange[] {
  const out: DiffChange[] = []
  for (const item of items) {
    if (item.kind !== "tool") continue
    const tool = item.data as ToolPayload
    for (const block of tool.content ?? []) {
      if ((block as ToolDiffBlock).type !== "diff") continue
      const diff = block as ToolDiffBlock
      const path = diff.path?.trim() || "change"
      out.push({ path, add: lineCount(diff.newText), del: lineCount(diff.oldText) })
    }
  }
  return out
}

export function diffAnchorId(path: string) {
  return `diff-${encodeURIComponent(path)}`
}
