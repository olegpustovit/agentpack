import DOMPurify from "dompurify"
import hljs from "highlight.js/lib/core"
import bash from "highlight.js/lib/languages/bash"
import css from "highlight.js/lib/languages/css"
import diff from "highlight.js/lib/languages/diff"
import go from "highlight.js/lib/languages/go"
import ini from "highlight.js/lib/languages/ini"
import json from "highlight.js/lib/languages/json"
import markdown from "highlight.js/lib/languages/markdown"
import python from "highlight.js/lib/languages/python"
import rust from "highlight.js/lib/languages/rust"
import sql from "highlight.js/lib/languages/sql"
import typescript from "highlight.js/lib/languages/typescript"
import xml from "highlight.js/lib/languages/xml"
import yaml from "highlight.js/lib/languages/yaml"
import { marked, type Token, type TokensList } from "marked"
import remend from "remend"

hljs.registerLanguage("go", go)
hljs.registerLanguage("typescript", typescript)
hljs.registerLanguage("javascript", typescript)
hljs.registerLanguage("bash", bash)
hljs.registerLanguage("sh", bash)
hljs.registerLanguage("json", json)
hljs.registerLanguage("css", css)
hljs.registerLanguage("diff", diff)
hljs.registerLanguage("ini", ini)
hljs.registerLanguage("toml", ini)
hljs.registerLanguage("markdown", markdown)
hljs.registerLanguage("python", python)
hljs.registerLanguage("rust", rust)
hljs.registerLanguage("sql", sql)
hljs.registerLanguage("xml", xml)
hljs.registerLanguage("html", xml)
hljs.registerLanguage("yaml", yaml)

const escapeHTML = (text: string) => text.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;")

marked.setOptions({ gfm: true, breaks: true })
marked.use({
  renderer: {
    html({ text }) {
      return escapeHTML(text)
    },
    code({ text, lang }) {
      const language = lang && hljs.getLanguage(lang) ? lang : undefined
      // Unlabelled fences stay plain: guessing a language runs every grammar over the block.
      const body = language ? hljs.highlight(text, { language }).value : escapeHTML(text)
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
export function renderAgentBlocks(text: string, streaming = false): string[] {
  const tokens = marked.lexer(streaming ? remend(text) : text) as TokensList
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
// Token spans only, so callers can place the result inside their own <pre> or line.
export function highlightCode(text: string, language: string): string {
  return DOMPurify.sanitize(hljs.highlight(text, { language }).value, {
    ALLOWED_TAGS: ["span"],
    ALLOWED_ATTR: ["class"],
  })
}

// Split sanitized token spans into balanced lines. Highlighting sees multiline tokens in context.
export function highlightCodeLines(text: string, language: string): string[] {
  const template = document.createElement("template")
  template.innerHTML = highlightCode(text, language)
  const escape = (value: string) => value.replaceAll("&", "&amp;").replaceAll("<", "&lt;").replaceAll(">", "&gt;").replaceAll('"', "&quot;")
  const split = (node: Node): string[] => {
    if (node.nodeType === Node.TEXT_NODE) return (node.textContent ?? "").split("\n").map(escape)
    const lines = [""]
    for (const child of node.childNodes) {
      const [first, ...rest] = split(child)
      lines[lines.length - 1] += first ?? ""
      lines.push(...rest)
    }
    const classes = node instanceof HTMLSpanElement ? node.className : ""
    return classes ? lines.map((line) => `<span class="${escape(classes)}">${line}</span>`) : lines
  }
  return split(template.content)
}
