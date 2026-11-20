import { diffLines, diffWordsWithSpace } from "diff"
import type { ReactNode } from "react"
import type { ToolDiffBlock } from "./model"
import { highlightCode } from "./agent-markdown"
import { diffAnchorId } from "./diff-summary"

function trimSplit(text: string): string[] {
  const lines = text.split("\n")
  if (lines.length > 1 && lines.at(-1) === "") lines.pop()
  return lines
}

function highlighted(text: string, language?: string): ReactNode {
  return language ? <span dangerouslySetInnerHTML={{ __html: highlightCode(text, language) }} /> : text
}

function delWords(oldLine: string, newLine: string, language?: string): ReactNode {
  return diffWordsWithSpace(oldLine, newLine)
    .filter((part) => !part.added)
    .map((part, index) =>
      part.removed ? (
        <mark key={index} className="rounded-(--r-row) bg-(--del-strong) px-px text-inherit">
          {highlighted(part.value, language)}
        </mark>
      ) : (
        highlighted(part.value, language)
      ),
    )
}

function addWords(oldLine: string, newLine: string, language?: string): ReactNode {
  return diffWordsWithSpace(oldLine, newLine)
    .filter((part) => !part.removed)
    .map((part, index) =>
      part.added ? (
        <mark key={index} className="rounded-(--r-row) bg-(--add-strong) px-px text-inherit">
          {highlighted(part.value, language)}
        </mark>
      ) : (
        highlighted(part.value, language)
      ),
    )
}

function diffLanguage(path?: string): string | undefined {
  const extension = path?.split(".").at(-1)?.toLowerCase()
  if (extension === "go") return "go"
  if (extension === "ts" || extension === "tsx") return "typescript"
  if (extension === "js" || extension === "jsx") return "javascript"
  if (extension === "sh" || extension === "bash") return "bash"
  if (extension === "json") return "json"
  return undefined
}

function DiffLine({
  kind,
  lineNo,
  sign,
  children,
  onComment,
  after,
}: {
  kind: "del" | "add"
  lineNo: number
  sign: string
  children: ReactNode
  onComment?: () => void
  after?: ReactNode
}) {
  const tone = kind === "del" ? "bg-(--del) text-(--danger-word)" : "bg-(--add) text-(--ok-word)"
  return (
    <>
      <div className={`group relative grid grid-cols-[2.5rem_1rem_1fr] gap-(--s2) px-(--s2) py-px font-(family-name:--mono) text-(length:--fs-xs) tabular-nums ${tone}`}>
        <span className="text-right text-(--fg3)">{lineNo}</span>
        <span>{sign}</span>
        <span className="min-w-0 whitespace-pre-wrap break-words">{children}</span>
        {onComment && (
          <button
            type="button"
            aria-label={`Comment on line ${lineNo}`}
            data-slot="diff-comment-add"
            className="absolute inset-y-0 left-0 flex w-(--s5) items-center justify-center rounded-(--r-row) bg-(--accent) text-(length:--fs-xs) text-white opacity-0 outline-none group-hover:opacity-100 focus-visible:opacity-100"
            onClick={onComment}
          >
            +
          </button>
        )}
      </div>
      {after}
    </>
  )
}

// Review hooks for one rendered diff. `key` names a line within this ToolDiffBody: block index, side and number.
export type DiffReview = {
  onComment: (key: string, target: { path: string; line: number }) => void
  after: (key: string) => ReactNode
}

function diffBlock(oldText: string, newText: string, language?: string, review?: { path: string; index: number; hooks: DiffReview }): ReactNode[] {
  const rows: ReactNode[] = []
  let oldNum = 1
  let newNum = 1
  const push = (kind: "del" | "add", lineNo: number, children: ReactNode) => {
    const key = `${kind === "del" ? "d" : "a"}-${lineNo}`
    const reviewKey = review ? `${review.index}:${key}` : ""
    rows.push(
      <DiffLine
        key={key}
        kind={kind}
        lineNo={lineNo}
        sign={kind === "del" ? "−" : "+"}
        onComment={review && (() => review.hooks.onComment(reviewKey, { path: review.path, line: lineNo }))}
        after={review?.hooks.after(reviewKey)}
      >
        {children}
      </DiffLine>,
    )
  }
  const changes = diffLines(oldText, newText)
  for (let index = 0; index < changes.length; index++) {
    const part = changes[index]!
    const next = changes[index + 1]
    if (part.removed && next?.added) {
      const oldLines = trimSplit(part.value)
      const newLines = trimSplit(next.value)
      const pairs = Math.max(oldLines.length, newLines.length)
      for (let line = 0; line < pairs; line++) {
        const oldLine = oldLines[line]
        const newLine = newLines[line]
        if (oldLine !== undefined && newLine !== undefined) {
          push("del", oldNum++, delWords(oldLine, newLine, language))
          push("add", newNum++, addWords(oldLine, newLine, language))
        } else if (oldLine !== undefined) {
          push("del", oldNum++, highlighted(oldLine, language))
        } else if (newLine !== undefined) {
          push("add", newNum++, highlighted(newLine, language))
        }
      }
      index++
      continue
    }
    if (part.removed) {
      for (const line of trimSplit(part.value)) push("del", oldNum++, highlighted(line, language))
      continue
    }
    if (part.added) {
      for (const line of trimSplit(part.value)) push("add", newNum++, highlighted(line, language))
    }
  }
  return rows
}

// With `review` this is the inspector's copy, which is not a scroll target, so it carries no anchor id.
export function ToolDiffBody({ content, toolId, review }: { content: ToolDiffBlock[] | undefined; toolId?: string; review?: DiffReview }) {
  const diffs = (content ?? []).filter((block) => block.type === "diff" && (block.oldText || block.newText))
  if (diffs.length === 0) return null
  return (
    <div className="mt-(--s2) flex flex-col gap-(--s3)">
      {diffs.map((block, index) => (
        <div key={index} id={block.path && !review ? diffAnchorId(block.path, toolId) : undefined} data-slot="tool-diff" className="flex flex-col overflow-hidden rounded-(--r-pop) bg-(--window) shadow-(--shadow-card)">
          {block.path && (
            <p className="border-b-[0.5px] border-(--sep) bg-(--code) px-(--s3) py-(--s2) font-(family-name:--mono) text-(length:--fs-xs) text-(--fg2)">{block.path}</p>
          )}
          <div className="flex flex-col gap-px overflow-x-auto py-(--s1)">
            {diffBlock(block.oldText ?? "", block.newText ?? "", diffLanguage(block.path), review && { path: block.path?.trim() || "change", index, hooks: review })}
          </div>
        </div>
      ))}
    </div>
  )
}

// For tests: true when word-level marks appear inside a changed line.
export function toolDiffUsesWordMarks(oldText: string, newText: string): boolean {
  const changes = diffLines(oldText, newText)
  for (let index = 0; index < changes.length; index++) {
    const part = changes[index]!
    const next = changes[index + 1]
    if (!part.removed || !next?.added) continue
    const oldLines = trimSplit(part.value)
    const newLines = trimSplit(next.value)
    for (let line = 0; line < Math.min(oldLines.length, newLines.length); line++) {
      const words = diffWordsWithSpace(oldLines[line]!, newLines[line]!)
      if (words.some((chunk) => chunk.added || chunk.removed)) return true
    }
    index++
  }
  return false
}
