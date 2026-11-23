import DOMPurify from "dompurify"
import { useSyncExternalStore } from "react"
import { marked, type Token, type TokensList } from "marked"

// highlight.js and remend load beside the chat instead of inside its chunk. Until they arrive,
// fences render plain and a streaming tail is parsed as typed; finished output re-renders once.
type Highlighter = typeof import("./highlight")
let highlighter: Highlighter | undefined
let mend: ((text: string) => string) | undefined
const listeners = new Set<() => void>()
export const markdownReady: Promise<void> = Promise.all([import("./highlight"), import("remend")]).then(([module, remend]) => {
  highlighter = module
  mend = remend.default
  renderedBlocks.clear()
  for (const listener of listeners) listener()
})

// The highlighter once loaded; pass it on so memoized renders follow the load.
export function useHighlighter(): Highlighter | undefined {
  return useSyncExternalStore((listener) => {
    listeners.add(listener)
    return () => listeners.delete(listener)
  }, () => highlighter)
}

const escapeHTML = (text: string) => text.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;")

marked.setOptions({ gfm: true, breaks: true })
marked.use({
  renderer: {
    html({ text }) {
      return escapeHTML(text)
    },
    code({ text, lang }) {
      // Unlabelled fences stay plain: guessing a language runs every grammar over the block.
      const highlighted = lang ? highlighter?.highlightFence(text, lang) : undefined
      const language = highlighter && highlighted === undefined ? undefined : lang || undefined
      const body = highlighted ?? escapeHTML(text)
      const cls = language ? `hljs language-${language}` : "hljs"
      const label = (language ?? "text").replace(/[^\w+-]/g, "")
      return `<div class="code-block"><div class="code-head"><span>${label}</span><button type="button" data-copy-code aria-label="Copy code">Copy</button></div><pre><code class="${cls}">${body}</code></pre></div>`
    },
  },
})

const sanitize = (html: string) => DOMPurify.sanitize(html, { FORBID_TAGS: ["img"], ALLOWED_URI_REGEXP: /^https?:/i })

export function renderAgentMarkdown(text: string): string {
  return sanitize(marked.parse(text, { async: false }) as string)
}

// One sanitized HTML string per top-level block. While streaming, remend closes half-typed
// syntax first, so a reply never flashes raw ** or an open fence. Finished blocks yield the same
// string on every chunk, so React leaves their DOM (and any selection in it) alone.
// `_highlighter` is the caller's useHighlighter() value, so a memoized render reruns when it loads.
export function renderAgentBlocks(text: string, streaming = false, _highlighter?: Highlighter): string[] {
  const tokens = marked.lexer(streaming && mend ? mend(text) : text) as TokensList
  const linked = Object.keys(tokens.links).length > 0
  const blocks: string[] = []
  for (const token of tokens as Token[]) {
    if (token.type === "space") continue
    // A block's HTML depends only on its source unless reference links are defined elsewhere.
    const cached = linked ? undefined : renderedBlocks.get(token.raw)
    if (cached !== undefined) {
      blocks.push(cached)
      continue
    }
    const list = [token] as unknown as TokensList
    list.links = tokens.links
    const html = sanitize(marked.parser(list))
    if (!linked) remember(token.raw, html)
    blocks.push(html)
  }
  return blocks
}

// Finished blocks are re-sent on every streamed chunk; parsing and sanitizing them once keeps a chunk O(new text).
const renderedBlocks = new Map<string, string>()
const maxRenderedBlocks = 2000

function remember(raw: string, html: string) {
  if (renderedBlocks.size >= maxRenderedBlocks) renderedBlocks.delete(renderedBlocks.keys().next().value!)
  renderedBlocks.set(raw, html)
}
