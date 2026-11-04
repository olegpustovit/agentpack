import { diffLines, diffWordsWithSpace } from "diff"
import type { ReactNode } from "react"
import type { ToolDiffBlock } from "./model"
import { highlightDiffCode } from "./agent-markdown"
import { diffAnchorId } from "./diff-summary"

function trimSplit(text: string): string[] {
  const lines = text.split("\n")
  if (lines.length > 1 && lines.at(-1) === "") lines.pop()
  return lines
}

function highlighted(text: string, language?: string): ReactNode {
  return language ? <span dangerouslySetInnerHTML={{ __html: highlightDiffCode(text, language) }} /> : text
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
}: {
  kind: "del" | "add"
  lineNo: number
  sign: string
  children: ReactNode
}) {
  const tone = kind === "del" ? "bg-(--del) text-(--danger-word)" : "bg-(--add) text-(--ok-word)"
  return (
    <div className={`grid grid-cols-[2.5rem_1rem_1fr] gap-(--s2) px-(--s2) py-px font-(family-name:--mono) text-(length:--fs-xs) tabular-nums ${tone}`}>
      <span className="text-right text-(--fg3)">{lineNo}</span>
      <span>{sign}</span>
      <span className="min-w-0 whitespace-pre-wrap break-words [&_.hljs-keyword]:text-(--accent) [&_.hljs-built_in]:text-(--accent) [&_.hljs-title]:text-(--accent) [&_.hljs-string]:text-(--ok-word) [&_.hljs-number]:text-(--warn-word) [&_.hljs-literal]:text-(--warn-word) [&_.hljs-comment]:text-(--fg3)">{children}</span>
    </div>
  )
}

function diffBlock(oldText: string, newText: string, language?: string): ReactNode[] {
  const rows: ReactNode[] = []
  let oldNum = 1
  let newNum = 1
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
          rows.push(
            <DiffLine key={`d-${oldNum}`} kind="del" lineNo={oldNum} sign="−">
              {delWords(oldLine, newLine, language)}
            </DiffLine>,
          )
          oldNum++
          rows.push(
            <DiffLine key={`a-${newNum}`} kind="add" lineNo={newNum} sign="+">
              {addWords(oldLine, newLine, language)}
            </DiffLine>,
          )
          newNum++
        } else if (oldLine !== undefined) {
          rows.push(
            <DiffLine key={`d-${oldNum}`} kind="del" lineNo={oldNum} sign="−">
              {highlighted(oldLine, language)}
            </DiffLine>,
          )
          oldNum++
        } else if (newLine !== undefined) {
          rows.push(
            <DiffLine key={`a-${newNum}`} kind="add" lineNo={newNum} sign="+">
              {highlighted(newLine, language)}
            </DiffLine>,
          )
          newNum++
        }
      }
      index++
      continue
    }
    if (part.removed) {
      for (const line of trimSplit(part.value)) {
        rows.push(
          <DiffLine key={`d-${oldNum}`} kind="del" lineNo={oldNum} sign="−">
            {highlighted(line, language)}
          </DiffLine>,
        )
        oldNum++
      }
      continue
    }
    if (part.added) {
      for (const line of trimSplit(part.value)) {
        rows.push(
          <DiffLine key={`a-${newNum}`} kind="add" lineNo={newNum} sign="+">
            {highlighted(line, language)}
          </DiffLine>,
        )
        newNum++
      }
    }
  }
  return rows
}

export function ToolDiffBody({ content }: { content: ToolDiffBlock[] | undefined }) {
  const diffs = (content ?? []).filter((block) => block.type === "diff" && (block.oldText || block.newText))
  if (diffs.length === 0) return null
  return (
    <div className="flex flex-col gap-(--s3) border-t-[0.5px] border-(--sep) px-(--s3) py-(--s2)">
      {diffs.map((block, index) => (
        <div key={index} id={block.path ? diffAnchorId(block.path) : undefined} data-slot="tool-diff" className="flex flex-col gap-(--s1) overflow-x-auto">
          {block.path && (
            <p className="font-(family-name:--mono) text-(length:--fs-xs) text-(--fg2)">{block.path}</p>
          )}
          <div className="flex flex-col gap-px rounded-(--r-row) border-[0.5px] border-(--sep) py-(--s1)">
            {diffBlock(block.oldText ?? "", block.newText ?? "", diffLanguage(block.path))}
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
