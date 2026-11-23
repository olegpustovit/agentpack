import { useState } from "react"
import { Button } from "@/components/ui/button"
import { useHighlighter } from "./agent-markdown"

// Lines shown before "Show all"; highlighting stops past HIGHLIGHT_LIMIT characters either way.
export const PREVIEW_LINES = 400
const HIGHLIGHT_LIMIT = 200_000

export type Tone = "error" | "ok" | "warn" | "note"
export type OutputLine = { n?: number; text: string; tone?: Tone }
export type ClassifiedOutput = { language?: string; lines: OutputLine[] }

const languageByExtension: Record<string, string> = {
  go: "go", ts: "typescript", tsx: "typescript", js: "typescript", jsx: "typescript", mjs: "typescript", cjs: "typescript",
  sh: "bash", bash: "bash", zsh: "bash", json: "json", jsonl: "json", py: "python", yaml: "yaml", yml: "yaml",
  html: "xml", xml: "xml", svg: "xml", css: "css", md: "markdown", toml: "toml", ini: "ini", rs: "rust", sql: "sql",
  diff: "diff", patch: "diff",
}

export function languageForPath(path: string | undefined): string | undefined {
  const name = path?.split(/[\\/]/).pop()?.toLowerCase() ?? ""
  const dot = name.lastIndexOf(".")
  return dot > 0 ? languageByExtension[name.slice(dot + 1)] : undefined
}

// "   12→code" (Claude Read) and "    12\tcode" (cat -n): the number moves to a gutter.
const NUMBERED = /^\s*(\d+)(?:→|\t)(.*)$/

const ERROR = /\b(error|errors|failed|failure|fatal|panic|exception|traceback|denied)\b|✘|✗|^FAIL\b|^--- FAIL/i
const OK = /^(ok|PASS)\b|^--- PASS|✓|\b\d+ passed\b/
const WARN = /\bwarn(ing)?s?\b|\bdeprecated\b/i
// Notes the harness appends to tool output, such as "[This command modified 1 file …]".
const NOTE = /^\[[^\]]+\]$/

function tone(line: string): Tone | undefined {
  if (NOTE.test(line.trim())) return "note"
  if (OK.test(line)) return "ok"
  if (ERROR.test(line)) return "error"
  if (WARN.test(line)) return "warn"
  return undefined
}

// Decides how a tool's output reads: code in a language (with an optional line gutter), or shell output
// with each line toned. Pure and linear in the text, so it is cheap to run on every render.
export function classifyOutput(text: string, hint: { path?: string } = {}): ClassifiedOutput {
  const raw = text.split("\n")
  const sample = raw.filter((line) => line.trim()).slice(0, 20)
  const numbered = sample.length > 0 && sample.filter((line) => NUMBERED.test(line)).length >= sample.length * 0.8
  const lines: OutputLine[] = numbered
    ? raw.map((line) => {
        const match = NUMBERED.exec(line)
        return match ? { n: Number(match[1]), text: match[2]! } : { text: line, tone: tone(line) }
      })
    : raw.map((line) => ({ text: line }))
  const body = lines.map((line) => line.text).join("\n")
  let language = languageForPath(hint.path)
  // A hunk header, a git header, or a ---/+++ pair; a lone "--- FAIL" from go test is not a diff.
  if (!language && (/^(diff --git |@@ -\d+(,\d+)? \+\d+)/m.test(body) || (/^--- \S/m.test(body) && /^\+\+\+ \S/m.test(body)))) language = "diff"
  if (!language && /^\s*[[{]/.test(body) && body.length < HIGHLIGHT_LIMIT) {
    try {
      JSON.parse(body)
      language = "json"
    } catch {
      // Not JSON: shell output.
    }
  }
  if (!language && !numbered) for (const line of lines) line.tone = tone(line.text)
  return { language, lines }
}

const toneClass: Record<Tone, string> = {
  error: "text-(--danger-word)",
  ok: "text-(--ok-word)",
  warn: "text-(--warn-word)",
  note: "text-(--fg3) italic",
}

const frame =
  "mt-(--s2) max-h-80 overflow-auto rounded-(--r-pop) bg-(--window) shadow-(--shadow-card) font-(family-name:--mono) text-(length:--fs-xs) leading-[1.5] text-(--fg)"

// Tool output: highlighted code with a line gutter, or toned shell output. Long output shows the
// first PREVIEW_LINES until asked; code past HIGHLIGHT_LIMIT stays plain to keep the thread responsive.
export function ToolOutput({ text, path, command, slot = "tool-output" }: { text: string; path?: string; command?: string; slot?: string }) {
  const [all, setAll] = useState(false)
  const highlighter = useHighlighter()
  const { language, lines } = classifyOutput(text, { path })
  const shown = all ? lines : lines.slice(0, PREVIEW_LINES)
  const hidden = lines.length - shown.length
  const gutter = shown.some((line) => line.n !== undefined)
  const code = shown.map((line) => line.text).join("\n")
  const html = highlighter && language && code.length <= HIGHLIGHT_LIMIT ? highlighter.highlightCode(code, language) : undefined
  return (
    <div data-slot={slot} data-language={language ?? "text"} className={frame}>
      {command && command.includes("\n") && (
        <pre className="border-b-[0.5px] border-(--sep) px-(--s4) py-(--s3) whitespace-pre-wrap break-words text-(--fg2)">
          <span aria-hidden className="select-none text-(--fg3)">$ </span>
          {highlighter ? <code dangerouslySetInnerHTML={{ __html: highlighter.highlightCode(command, "bash") }} /> : <code>{command}</code>}
        </pre>
      )}
      <div className={gutter || html !== undefined ? "flex min-w-max" : undefined}>
        {gutter && (
          <pre aria-hidden className="sticky left-0 shrink-0 border-r-[0.5px] border-(--sep) bg-(--window) px-(--s3) py-(--s3) text-right text-(--fg3) tabular-nums select-none">
            {shown.map((line) => line.n ?? "").join("\n")}
          </pre>
        )}
        {html !== undefined ? (
          <pre className="px-(--s4) py-(--s3)"><code dangerouslySetInnerHTML={{ __html: html }} /></pre>
        ) : (
          <pre className={gutter ? "px-(--s4) py-(--s3)" : "px-(--s4) py-(--s3) whitespace-pre-wrap break-words"}>
            {shown.map((line, index) => (
              <span key={index} className={line.tone && toneClass[line.tone]}>
                {line.text}
                {index < shown.length - 1 && "\n"}
              </span>
            ))}
          </pre>
        )}
      </div>
      {hidden > 0 && (
        <div className="sticky left-0 border-t-[0.5px] border-(--sep) px-(--s4) py-(--s2)">
          <Button size="sm" variant="ghost" onClick={() => setAll(true)}>
            Show all {lines.length.toLocaleString()} lines
          </Button>
        </div>
      )}
    </div>
  )
}
