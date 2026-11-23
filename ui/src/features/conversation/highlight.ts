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

// Loaded on demand (agent-markdown.ts): the grammars are a quarter of the chat chunk.
export function highlightFence(text: string, language: string): string | undefined {
  return hljs.getLanguage(language) ? hljs.highlight(text, { language }).value : undefined
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
