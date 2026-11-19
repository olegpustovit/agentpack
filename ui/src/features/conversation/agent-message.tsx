import type { MouseEvent } from "react"
import { hud } from "@/lib/hud"
import { renderAgentBlocks } from "./agent-markdown"
import { stripAgentText } from "./agent-text"

const mdClass =
  "md max-w-none text-(length:--fs-sm) text-(--fg) [&>*+*]:mt-(--s4) [&_ol]:list-decimal [&_ol]:pl-(--s7) [&_ul]:list-disc [&_ul]:pl-(--s7) [&_li+li]:mt-(--s1) [&_li>ol]:mt-(--s1) [&_li>ul]:mt-(--s1) [&_h1]:font-semibold [&_h2]:font-semibold [&_h3]:font-semibold [&_blockquote]:border-l-2 [&_blockquote]:border-(--sep) [&_blockquote]:pl-(--s5) [&_blockquote]:text-(--fg2) [&_a]:text-(--accent) [&_code]:rounded-(--r-row) [&_code]:bg-(--track) [&_code]:px-(--s1) [&_pre]:overflow-x-auto [&_pre]:rounded-(--r-row) [&_pre]:bg-(--track) [&_pre]:p-(--s3) [&_.code-block]:overflow-hidden [&_.code-block]:rounded-(--r-row) [&_.code-block]:bg-(--track) [&_.code-block_pre]:rounded-none [&_.code-block_pre]:pt-0 [&_.code-head]:flex [&_.code-head]:items-center [&_.code-head]:justify-between [&_.code-head]:px-(--s3) [&_.code-head]:py-(--s1) [&_.code-head]:text-(length:--fs-xs) [&_.code-head]:text-(--fg3) [&_.code-head_button]:rounded-(--r-row) [&_.code-head_button]:px-(--s2) [&_.code-head_button:hover]:bg-(--hover) [&_.code-head_button:hover]:text-(--fg)"

// Copy buttons live inside sanitized HTML, so one delegated handler serves them all.
function copyCode(event: MouseEvent<HTMLDivElement>) {
  const button = (event.target as HTMLElement).closest("[data-copy-code]")
  const code = button?.closest(".code-block")?.querySelector("code")?.textContent
  if (!button || code === undefined || code === null) return
  ;(navigator.clipboard?.writeText(code) ?? Promise.reject()).then(
    () => hud.success("Copied"),
    () => hud.error("Couldn't copy. Select the code and copy it manually."),
  )
}

export function AgentMessageBody({ text, streaming = false }: { text: string; streaming?: boolean }) {
  const cleaned = stripAgentText(text)
  let blocks: string[] = []
  if (cleaned) {
    try {
      blocks = renderAgentBlocks(cleaned, streaming)
    } catch {
      blocks = []
    }
  }
  if (blocks.length > 0) {
    return (
      <div className={mdClass} onClick={copyCode}>
        {/* Keyed by position: a finished block's HTML never changes, so React keeps its DOM and selection. */}
        {blocks.map((html, index) => <div key={index} dangerouslySetInnerHTML={{ __html: html }} />)}
      </div>
    )
  }
  return <p className="text-(length:--fs-sm) text-(--fg) whitespace-pre-wrap">{cleaned}</p>
}
