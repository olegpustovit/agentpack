import { FileCode2 } from "lucide-react"
import { Meter, Section, KeyValue } from "@/components/kit/data"
import type { SessionInfo } from "@/lib/api-types"
import { harnessName } from "@/lib/format"
import { hud } from "@/lib/hud"
import { cn } from "@/lib/utils"
import type { ThreadState } from "./model"
import { diffAnchorId, listDiffChanges } from "./diff-summary"

export function ConversationInspector({
  thread,
  info,
  machine,
  open,
}: {
  thread: ThreadState
  info?: SessionInfo
  machine: string
  open: boolean
}) {
  const actor = info?.started_by
  const startedBy = actor ? [actor.user ?? actor.node, actor.kind].filter(Boolean).join(" · ") : undefined
  const capabilities = info?.agent_capabilities
  const imageSupport = capabilities?.promptCapabilities?.image
  const sessionCapabilities = capabilities?.sessionCapabilities
  const resumeSupport =
    capabilities?.loadSession === true ||
    (sessionCapabilities?.resume !== undefined && sessionCapabilities.resume !== null)
  const forkSupport =
    sessionCapabilities?.fork !== undefined && sessionCapabilities.fork !== null
  const contextLabel =
    thread.context_limit && thread.context_tokens !== undefined
      ? `${thread.context_tokens.toLocaleString()} of ${thread.context_limit.toLocaleString()}`
      : undefined
  const contextPercent =
    thread.context_limit && thread.context_tokens !== undefined
      ? Math.min(100, Math.round((thread.context_tokens / thread.context_limit) * 100))
      : undefined

  const changes = listDiffChanges(thread.items)
  return (
    <aside
      data-slot="conversation-inspector"
      aria-label="Session details"
      hidden={!open}
      className={cn(
        "shrink-0 flex-col overflow-auto border-l-[0.5px] border-(--sep) bg-(--sidebar) w-(--inspector-w) gap-(--s6) p-(--s5)",
        open ? "flex" : "hidden",
        open && "max-[1040px]:absolute max-[1040px]:inset-y-0 max-[1040px]:right-0 max-[1040px]:z-30 max-[1040px]:max-w-[calc(100vw-64px)]",
      )}
      style={{ ["--inspector-w" as string]: "272px" }}
    >
      <div id="conversation-inspector-content" className="flex min-w-0 flex-col gap-(--s6)">
        {contextLabel && (
          <Section title="Context">
            {contextPercent !== undefined && (
              <Meter value={thread.context_tokens} max={thread.context_limit} label={`${contextPercent}% of context used`} />
            )}
            <dl className="flex flex-col gap-(--s3)">
              <KeyValue label="Used">{contextLabel}</KeyValue>
              {thread.cost_usd !== undefined && thread.cost_usd > 0 && (
                <KeyValue label="Cost so far">${thread.cost_usd.toFixed(2)}</KeyValue>
              )}
            </dl>
          </Section>
        )}
        {changes.length > 0 && (
          <Section title="Changes">
            <ul className="flex flex-col gap-(--s2)">
              {changes.map((change) => {
                const file = change.path.split("/").pop() ?? change.path
                return (
                  <li key={change.path}>
                    <button
                      type="button"
                      className="flex w-full items-center gap-(--s2) rounded-(--r-row) border-[0.5px] border-(--sep) bg-(--window) px-(--s3) py-(--s2) text-left text-(length:--fs-sm) hover:bg-(--hover)"
                      onClick={() => {
                        const target = document.getElementById(diffAnchorId(change.path))
                        if (target) {
                          target.scrollIntoView({ behavior: "smooth", block: "center" })
                        } else {
                          hud.error("Couldn't find that diff in the thread.")
                        }
                      }}
                    >
                      <FileCode2 aria-hidden className="size-(--s4) shrink-0 text-(--fg2)" />
                      <code className="min-w-0 flex-1 truncate font-(family-name:--mono) text-(length:--fs-xs)">{file}</code>
                      <span className="shrink-0 tabular-nums text-(--ok-word)">+{change.add}</span>
                      <span className="shrink-0 tabular-nums text-(--danger-word)">−{change.del}</span>
                    </button>
                  </li>
                )
              })}
            </ul>
          </Section>
        )}
        {info && (
          <Section title="Session">
            <dl className="flex flex-col gap-(--s3)">
              <KeyValue label="Agent">{harnessName(info.harness)}</KeyValue>
              {startedBy && <KeyValue label="Started by">{startedBy}</KeyValue>}
              <KeyValue label="Machine">{machine}</KeyValue>
              <KeyValue label="Project">{info.directory.split("/").pop() || info.directory}</KeyValue>
              <KeyValue label="Mode">{info.mode || "default"}</KeyValue>
            </dl>
          </Section>
        )}
        {capabilities && (
          <Section title="Agent capabilities">
            <dl className="flex flex-col gap-(--s3)">
              <KeyValue label="Images">{imageSupport === true ? "Yes" : "No"}</KeyValue>
              <KeyValue label="Resume">{resumeSupport ? "Yes" : "No"}</KeyValue>
              <KeyValue label="Fork">{forkSupport ? "Yes" : "No"}</KeyValue>
            </dl>
          </Section>
        )}
      </div>
    </aside>
  )
}
