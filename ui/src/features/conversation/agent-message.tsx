import { renderAgentMarkdown } from "./agent-markdown"
import { stripAgentText } from "./agent-text"

export function AgentMessageBody({ text }: { text: string }) {
  const cleaned = stripAgentText(text)
  let html = ""
  if (cleaned) {
    try {
      html = renderAgentMarkdown(cleaned)
    } catch {
      html = ""
    }
  }
  const plain = cleaned
  if (html) {
    return (
      <div
        className="prose prose-sm max-w-none text-(length:--fs-sm) text-(--fg) [&_a]:text-(--accent) [&_code]:rounded-(--r-row) [&_code]:bg-(--track) [&_code]:px-(--s1) [&_pre]:overflow-x-auto [&_pre]:rounded-(--r-row) [&_pre]:bg-(--track) [&_pre]:p-(--s3)"
        dangerouslySetInnerHTML={{ __html: html }}
      />
    )
  }
  return <p className="text-(length:--fs-sm) text-(--fg) whitespace-pre-wrap">{plain}</p>
}
