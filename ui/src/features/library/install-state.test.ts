import { describe, expect, it } from "vitest"
import type { ProjectRow } from "@/lib/api-types"
import { affectedAgents, canApplyDraft, destinationState, expectedPins, installLabel, restartCopy, restorePlan, runLimited, type Destination, type InstallItem } from "./install-state"
import type { DraftReview } from "./mutations"

const item: InstallItem = { module: "github.com/acme/alpha", commit: "abcd1234ef56", name: "alpha", cache_key: "key-1" }

function project(overrides: Partial<ProjectRow> = {}): ProjectRow {
  return {
    directory: "/srv/app",
    name: "app",
    folder_id: "folder-1",
    available: true,
    unavailable_reason: "",
    last_used: "",
    launches: [],
    packages: [],
    mcp: [],
    error: "",
    ...overrides,
  }
}

function target(overrides: Omit<Partial<Destination>, "project"> & { project?: Partial<ProjectRow> } = {}): Destination {
  const { project: projectOverrides, ...rest } = overrides
  return { machine: "local", machineName: "host-a", online: true, ...rest, project: project(projectOverrides) }
}

describe("destinationState", () => {
  it("installs into a project that does not pin the package yet", () => {
    expect(destinationState(item, target())).toEqual({ disabled: false, draftReason: "", text: "Not installed" })
  })

  it("names the pin change with seven characters on each side", () => {
    const pinned = target({ project: { packages: [{ module: item.module, commit: "1111111aaaa", content_hash: "", cache_key: "", kind: "skill", name: "alpha", url: "", direct: true }] } })
    expect(destinationState(item, pinned).text).toBe("Update 1111111 → abcd123")
  })

  it("disables a project already on this commit", () => {
    const same = target({ project: { packages: [{ module: item.module, commit: item.commit, content_hash: "", cache_key: "", kind: "skill", name: "alpha", url: "", direct: true }] } })
    expect(destinationState(item, same)).toEqual({ disabled: true, draftReason: "", text: "Installed" })
  })

  it("saves a draft for an offline machine that has a folder identity", () => {
    expect(destinationState(item, target({ online: false }))).toEqual({ disabled: false, draftReason: "Machine offline", text: "Offline · install as draft" })
  })

  it("disables an offline machine whose folder has no identity", () => {
    expect(destinationState(item, target({ online: false, project: { folder_id: "" } })).disabled).toBe(true)
  })

  it("drafts an unavailable folder and keeps its reason", () => {
    const state = destinationState(item, target({ project: { available: false, unavailable_reason: "Folder moved" } }))
    expect(state).toEqual({ disabled: false, draftReason: "Folder moved", text: "Folder unavailable · Folder moved" })
  })

  it("disables a project whose pins could not be checked", () => {
    expect(destinationState(item, target({ project: { error: "timeout" } }))).toEqual({ disabled: true, draftReason: "", text: "Could not check pins · timeout" })
  })
})

describe("expectedPins", () => {
  it("pins every package by commit and cache key", () => {
    expect(expectedPins([item])).toEqual({ [item.module]: { commit: item.commit, cache_key: "key-1" } })
  })

  it("refuses a package without its exact identity", () => {
    expect(() => expectedPins([{ ...item, cache_key: "" }])).toThrow("Exact package identity is unavailable for github.com/acme/alpha.")
  })
})

describe("restorePlan", () => {
  const pack = [item]
  it("restores into a checked project", () => {
    expect(restorePlan(target(), pack).kind).toBe("restore")
  })

  it("saves a draft for an offline or unchecked project with a folder identity", () => {
    expect(restorePlan(target({ online: false }), pack)).toEqual({ kind: "draft", text: "Machine offline · save as draft", reason: "Machine offline" })
    expect(restorePlan(target({ project: { error: "timeout" } }), pack).kind).toBe("draft")
  })

  it("blocks a project that cannot be drafted", () => {
    expect(restorePlan(target({ online: false, project: { folder_id: "" } }), pack)).toEqual({ kind: "blocked", text: "Offline", reason: "Machine offline" })
  })
})

describe("runLimited", () => {
  it("keeps at most two installs in flight per machine, with machines running side by side", async () => {
    const active = new Map<string, number>()
    const peak = new Map<string, number>()
    const jobs = ["a", "b", "c", "d", "e"].flatMap((name) => [
      { machine: "m1", name },
      { machine: "m2", name },
    ])
    const done: string[] = []
    await runLimited(
      jobs,
      (job) => job.machine,
      2,
      async (job) => {
        const running = (active.get(job.machine) ?? 0) + 1
        active.set(job.machine, running)
        peak.set(job.machine, Math.max(peak.get(job.machine) ?? 0, running))
        for (let step = 0; step < 3; step++) await Promise.resolve()
        active.set(job.machine, (active.get(job.machine) ?? 0) - 1)
        done.push(`${job.machine}:${job.name}`)
      },
    )
    expect(peak.get("m1")).toBe(2)
    expect(peak.get("m2")).toBe(2)
    expect(done).toHaveLength(jobs.length)
  })
})

describe("canApplyDraft", () => {
  const review = (overrides: Partial<DraftReview>): DraftReview => ({
    draft: {} as DraftReview["draft"],
    ready: true,
    pins_match: true,
    folder_match: true,
    reason: "",
    ...overrides,
  })

  it("applies only a review that is ready and matches every pin and folder", () => {
    expect(canApplyDraft(review({}))).toBe(true)
    expect(canApplyDraft(review({ pins_match: false, reason: "Pins changed" }))).toBe(false)
    expect(canApplyDraft(review({ folder_match: false }))).toBe(false)
    expect(canApplyDraft(review({ ready: false }))).toBe(false)
  })
})

describe("copy", () => {
  it("shows no restart line and no affected count at zero", () => {
    expect(restartCopy(0)).toBe("")
    expect(restartCopy(2)).toBe("Restarts 2 agents")
    expect(affectedAgents([], "/srv/app")).toBe(0)
  })

  it("reads Install when nothing is chosen, then names what the primary does", () => {
    expect(installLabel("pick", 0, 0)).toBe("Install")
    expect(installLabel("pick", 2, 0)).toBe("Install in 2 projects")
    expect(installLabel("pick", 0, 1)).toBe("Save 1 draft")
    expect(installLabel("pick", 1, 1)).toBe("Install 1 · Draft 1")
  })
})
