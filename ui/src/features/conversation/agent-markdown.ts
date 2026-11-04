import DOMPurify from "dompurify"
import hljs from "highlight.js/lib/core"
import bash from "highlight.js/lib/languages/bash"
import go from "highlight.js/lib/languages/go"
import json from "highlight.js/lib/languages/json"
import typescript from "highlight.js/lib/languages/typescript"
import { marked } from "marked"

hljs.registerLanguage("go", go)
hljs.registerLanguage("typescript", typescript)
hljs.registerLanguage("javascript", typescript)
hljs.registerLanguage("bash", bash)
hljs.registerLanguage("sh", bash)
hljs.registerLanguage("json", json)

marked.setOptions({ gfm: true, breaks: true })
marked.use({
  renderer: {
    html({ text }) {
      return text.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;")
    },
    code({ text, lang }) {
      const language = lang && hljs.getLanguage(lang) ? lang : undefined
      const body = language
        ? hljs.highlight(text, { language }).value
        : hljs.highlightAuto(text, ["go", "typescript", "bash", "json"]).value
      const cls = language ? `hljs language-${language}` : "hljs"
      return `<pre><code class="${cls}">${body}</code></pre>`
    },
  },
})

export function renderAgentMarkdown(text: string): string {
  const html = marked.parse(text, { async: false }) as string
  return DOMPurify.sanitize(html, {
    FORBID_TAGS: ["img"],
    ALLOWED_URI_REGEXP: /^https?:/i,
  })
}
export function highlightDiffCode(text: string, language: string): string {
  return DOMPurify.sanitize(hljs.highlight(text, { language }).value, {
    ALLOWED_TAGS: ["span"],
    ALLOWED_ATTR: ["class"],
  })
}
