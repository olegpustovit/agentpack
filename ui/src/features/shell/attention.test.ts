import { describe, expect, it } from "vitest"
import type { ProviderGroup } from "@/features/limits/model"
import type { ListedSession } from "@/features/sessions/group"
import { attentionAlerts, attentionTitle, needsYouCount, watch } from "./attention"

function chat(id: string, conversation_state: string): ListedSession {
  return {
    id,
    directory: "/work/alpha",
    harness: "claude",
    mode: "default",
    transport: "acp",
    conversation_state,
    state: { interactive: true, running: true, restarting: false, rows: 30, columns: 100, exit_code: 0 },
    machine: "local",
    machineName: "laptop",
    offline: false,
  } as ListedSession
}

function limit(left: number): ProviderGroup[] {
  const snapshot = { harness: "codex", state: "available", observed_at: "", windows: [{ remaining_percent: left }] }
  return [{ provider: "codex", selected: { id: "local", name: "laptop", snapshot }, others: [], usable: true }] as unknown as ProviderGroup[]
}

const vm = (online: boolean) => [{ id: "local", name: "laptop", online: true }, { id: "vm", name: "vm", online }]

describe("attentionAlerts", () => {
  it("alerts on needs you, a finished turn, a limit crossing 20% and a machine going offline", () => {
    const before = watch([chat("a", "idle"), chat("b", "working")], limit(30), vm(true))
    const after = watch([chat("a", "needs_you"), chat("b", "idle")], limit(18), vm(false))
    expect(attentionAlerts(before, after).map((alert) => [alert.kind, alert.title])).toEqual([
      ["needs", "Claude needs you"],
      ["finished", "Claude finished"],
      ["limit", "Codex limit is low"],
      ["offline", "vm went offline"],
    ])
    expect(needsYouCount(after)).toBe(1)
  })

  it("stays quiet when nothing crossed, including a limit already low and the local gateway", () => {
    const before = watch([chat("a", "needs_you")], limit(15), vm(false))
    const after = watch([chat("a", "needs_you")], limit(10), [{ id: "local", name: "laptop", online: false }, { id: "vm", name: "vm", online: false }])
    expect(attentionAlerts(before, after)).toEqual([])
  })

  it("counts needs you in the title", () => {
    expect(attentionTitle(0)).toBe("Agentpack")
    expect(attentionTitle(2)).toBe("(2) Agentpack")
  })
})
