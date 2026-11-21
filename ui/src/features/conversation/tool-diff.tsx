import { diffLines, diffWordsWithSpace } from "diff"
import type { ReactNode } from "react"
import type { ToolDiffBlock } from "./model"
import { highlightCodeLines } from "./agent-markdown"
import { diffAnchorId } from "./diff-summary"

function trimSplit(text: string): string[] {
  const lines = text.split("\n")
  if (lines.length > 1 && lines.at(-1) === "") lines.pop()
  return lines
}

const MAX_DIFF_ROWS = 400

function highlighted(lines: string[], language?: string): string[] | undefined {
  const text = lines.join("\n")
  return language && text.length <= 200_000 ? highlightCodeLines(text, language) : undefined
}

function codeLine(text: string, html: string | undefined): ReactNode {
  return html === undefined ? text : <span dangerouslySetInnerHTML={{ __html: html }} />
}

function markedWords(oldLine: string, newLine: string, remove: boolean, html?: string): ReactNode {
  const text = remove ? oldLine : newLine
  if (oldLine.length + newLine.length > 8192) return codeLine(text, html)
  const className = `rounded-(--r-row) ${remove ? "bg-(--del-strong)" : "bg-(--add-strong)"} px-px text-inherit`
  const parts = diffWordsWithSpace(oldLine, newLine).filter((part) => remove ? !part.added : !part.removed)
  if (html === undefined) {
    return parts.map((part, index) =>
      (remove ? part.removed : part.added) ? <mark key={index} className={className}>{part.value}</mark> : part.value,
    )
  }
  const template = document.createElement("template")
  template.innerHTML = html
  const walker = document.createTreeWalker(template.content, NodeFilter.SHOW_TEXT)
  const nodes: Text[] = []
  while (walker.nextNode()) nodes.push(walker.currentNode as Text)
  let partIndex = 0
  let partOffset = 0
  for (const node of nodes) {
    const replacement = document.createDocumentFragment()
    let offset = 0
    while (offset < node.length) {
      const part = parts[partIndex]
      if (!part) return codeLine(text, html)
      const size = Math.min(part.value.length - partOffset, node.length - offset)
      const value = node.data.slice(offset, offset + size)
      if (remove ? part.removed : part.added) {
        const mark = document.createElement("mark")
        mark.className = className
        mark.textContent = value
        replacement.append(mark)
      } else {
        replacement.append(value)
      }
      offset += size
      partOffset += size
      if (partOffset === part.value.length) {
        partIndex++
        partOffset = 0
      }
    }
    node.replaceWith(replacement)
  }
  return codeLine(text, template.innerHTML)
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
  for (let index = 0; index < changes.length && rows.length < MAX_DIFF_ROWS; index++) {
    const part = changes[index]!
    const next = changes[index + 1]
    if (part.removed && next?.added) {
      const remaining = MAX_DIFF_ROWS - rows.length
      const oldLines = trimSplit(part.value).slice(0, remaining)
      const newLines = trimSplit(next.value).slice(0, remaining)
      const oldHighlight = highlighted(oldLines, language)
      const newHighlight = highlighted(newLines, language)
      const pairs = Math.max(oldLines.length, newLines.length)
      for (let line = 0; line < pairs && rows.length < MAX_DIFF_ROWS; line++) {
        const oldLine = oldLines[line]
        const newLine = newLines[line]
        if (oldLine !== undefined && newLine !== undefined) {
          push("del", oldNum++, markedWords(oldLine, newLine, true, oldHighlight?.[line]))
          if (rows.length < MAX_DIFF_ROWS) push("add", newNum++, markedWords(oldLine, newLine, false, newHighlight?.[line]))
        } else if (oldLine !== undefined) {
          push("del", oldNum++, codeLine(oldLine, oldHighlight?.[line]))
        } else if (newLine !== undefined) {
          push("add", newNum++, codeLine(newLine, newHighlight?.[line]))
        }
      }
      index++
      continue
    }
    if (part.removed) {
      const lines = trimSplit(part.value).slice(0, MAX_DIFF_ROWS - rows.length)
      const content = highlighted(lines, language)
      lines.forEach((line, index) => push("del", oldNum++, codeLine(line, content?.[index])))
      continue
    }
    if (part.added) {
      const lines = trimSplit(part.value).slice(0, MAX_DIFF_ROWS - rows.length)
      const content = highlighted(lines, language)
      lines.forEach((line, index) => push("add", newNum++, codeLine(line, content?.[index])))
    }
  }
  if (rows.length >= MAX_DIFF_ROWS) {
    rows.push(<p key="limit" className="px-(--s3) py-(--s2) text-(length:--fs-xs) text-(--fg3)">Showing the first {MAX_DIFF_ROWS} changed lines</p>)
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
