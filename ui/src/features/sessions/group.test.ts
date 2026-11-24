import { describe, expect, it } from "vitest"
import { groupSessions, matchesSession, nestHelpers, sessionDetail, sessionLabel, treeSessions, type ListedSession } from "./group"

function session(id: string, machine: string, machineName: string, directory: string, offline = false): ListedSession {
  return {
    id,
    directory,
    harness: "claude",
    mode: "default",
    state: { interactive: true, running: true, restarting: false, rows: 30, columns: 100, exit_code: 0 },
    machine,
    machineName,
    offline,
  }
}

const laptop = "laptop"
const vm = "vm"

describe("groupSessions", () => {
  it("a worktree session groups under its main folder's project", () => {
    const worktree = { ...session("w", laptop, "laptop", "/home/me/.local/share/agentpack/worktrees/0123456789abcdef/replay-offset"), definition_root: "/work/agentpack" }
    const groups = groupSessions([session("a", laptop, "laptop", "/work/agentpack"), worktree], "project")
    expect(groups.map((group) => group.label)).toEqual(["agentpack"])
    expect(groups[0]?.subheads.map((head) => head.sessions.map((item) => item.id))).toEqual([["a", "w"]])
    // A monorepo subfolder that names its parent as definition root keeps its own folder.
    const sub = { ...session("s", laptop, "laptop", "/work/mono/web"), definition_root: "/work/mono" }
    expect(groupSessions([sub], "project").map((group) => group.label)).toEqual(["web"])
  })

  it("machine mode: one group per machine, project subheads under it", () => {
    const groups = groupSessions(
      [
        session("a", laptop, "laptop", "/work/agentpack"),
        session("b", laptop, "laptop", "/work/oracle"),
        session("c", vm, "vm", "/srv/agentpack"),
      ],
      "machine",
    )
    expect(groups.map((group) => group.label)).toEqual(["laptop", "vm"])
    expect(groups[0]?.subheads.map((head) => head.label)).toEqual(["agentpack", "oracle"])
    expect(groups[1]?.subheads.map((head) => head.label)).toEqual(["agentpack"])
  })

  it("project mode merges one folder name across machines, with machine subheads", () => {
    const groups = groupSessions(
      [
        session("a", laptop, "laptop", "/work/agentpack"),
        session("c", vm, "vm", "/srv/agentpack"),
        session("b", laptop, "laptop", "/work/oracle"),
      ],
      "project",
    )
    expect(groups.map((group) => group.label)).toEqual(["agentpack", "oracle"])
    const agentpack = groups[0]
    expect(agentpack?.subheads.map((head) => head.label)).toEqual(["laptop", "vm"])
    expect(agentpack?.subheads.map((head) => head.sessions.map((item) => item.id))).toEqual([["a"], ["c"]])
  })

  it("shows the parent folder only where one machine has two directories with the same basename", () => {
    const sessions = [
      session("a", laptop, "laptop", "/home/me/work/agentpack"),
      session("b", laptop, "laptop", "/home/me/oss/agentpack"),
      session("c", vm, "vm", "/srv/agentpack"),
    ]
    const machineMode = groupSessions(sessions, "machine")
    expect(machineMode[0]?.subheads.map((head) => head.parent)).toEqual(["oss", "work"])
    expect(machineMode[1]?.subheads[0]?.parent).toBeNull()

    const projectMode = groupSessions(sessions, "project")
    const agentpack = projectMode.find((group) => group.label === "agentpack")
    expect(agentpack?.subheads.map((head) => [head.label, head.parent])).toEqual([
      ["laptop", "oss"],
      ["laptop", "work"],
      ["vm", null],
    ])
  })

  it("treats a trailing separator as the same directory", () => {
    const groups = groupSessions(
      [session("a", laptop, "laptop", "/work/agentpack/"), session("b", laptop, "laptop", "/work/agentpack")],
      "machine",
    )
    expect(groups[0]?.subheads).toHaveLength(1)
    expect(groups[0]?.subheads[0]?.sessions).toHaveLength(2)
  })

  it("keeps an offline machine's sessions and marks its group offline", () => {
    const groups = groupSessions([session("a", vm, "vm", "/srv/agentpack", true)], "machine")
    expect(groups[0]?.offline).toBe(true)
    expect(groups[0]?.subheads[0]?.sessions[0]?.id).toBe("a")
  })
})

describe("subhead targets", () => {
  it("names the exact folder and machine a subhead starts in, without a trailing separator", () => {
    const [group] = groupSessions([session("a", vm, "vm", "/srv/agentpack/", true)], "project")
    expect(group!.subheads[0]).toMatchObject({ machine: vm, directory: "/srv/agentpack", offline: true })
  })

  it("gives a project on one machine a single subhead, which the list folds into the project row", () => {
    const groups = groupSessions([session("a", laptop, "laptop", "/work/oracle"), session("b", laptop, "laptop", "/work/oracle")], "project")
    expect(groups[0]?.subheads.map((head) => [head.label, head.sessions.length])).toEqual([["laptop", 2]])
  })
})

describe("matchesSession", () => {
  const row = { name: "feed-parser", harness: "claude", mode: "review", directory: "/home/o/rssdude", machineName: "oracle-vm" } as ListedSession
  it.each(["", "FEED", "claude", "review", "rssdude", "oracle"])("matches %j", (query) => {
    expect(matchesSession(row, query)).toBe(true)
  })
  it("rejects anything else", () => {
    expect(matchesSession(row, "grok")).toBe(false)
  })
})

describe("sessionLabel", () => {
  it("prefers name, then title, then the harness", () => {
    const base = session("a", laptop, "laptop", "/work/x")
    expect(sessionLabel({ ...base, name: "Mine", title: "Fix it" })).toBe("Mine")
    expect(sessionLabel({ ...base, title: "Fix it" })).toBe("Fix it")
    expect(sessionLabel({ ...base, transport: "acp" })).toBe("Claude")
    expect(sessionLabel(base)).toBe("Terminal · Claude")
    expect(matchesSession({ ...base, title: "Fix terminal replay" }, "replay")).toBe(true)
  })
})

describe("nestHelpers", () => {
  it("lists helpers under the chat whose agent started them", () => {
    const helper = { ...session("h", laptop, "laptop", "/w"), parent_id: "p", started_by: { kind: "browser", user: "a@b.c", agent: "claude" } }
    const grandchild = { ...session("g", laptop, "laptop", "/w"), parent_id: "h" }
    const orphan = { ...session("o", laptop, "laptop", "/w"), parent_id: "gone" }
    const nested = nestHelpers([grandchild, helper, session("x", laptop, "laptop", "/w"), session("p", laptop, "laptop", "/w"), orphan])
    expect(nested.map((entry) => `${entry.session.id}${entry.nested ? "+" : ""}`)).toEqual(["x", "p", "h+", "g+", "o"])
    expect(sessionDetail(helper)).toContain("started by Claude")
  })

  it("places a helper with its parent whatever its agent, folder or machine", () => {
    const parent = session("p", laptop, "laptop", "/repo")
    const helper = { ...session("h", "desk", "desk", "/elsewhere/worktree"), harness: "codex", parent_id: "p" }
    for (const mode of ["machine", "project"] as const) {
      const groups = groupSessions([helper, parent], mode)
      expect(groups).toHaveLength(1)
      const heads = groups[0]!.subheads
      expect(heads).toHaveLength(1)
      expect(nestHelpers(heads[0]!.sessions).map((entry) => `${entry.session.id}${entry.nested ? "+" : ""}`)).toEqual(["p", "h+"])
    }
  })
})

describe("treeSessions", () => {
  const lead = { ...session("p", laptop, "laptop", "/work/a"), transport: "acp", conversation_state: "idle" }
  const helper = (state: string, extra: Partial<ListedSession> = {}) => ({ ...session("h", laptop, "laptop", "/work/a"), transport: "acp", parent_id: "p", conversation_state: state, ...extra })
  const ids = (rows: ListedSession[]) => rows.map((row) => row.id)

  it("lists a working, asking or starting helper", () => {
    for (const state of ["working", "needs_you", "starting"]) expect(ids(treeSessions([lead, helper(state)]))).toEqual(["p", "h"])
  })

  it("drops a stopped or ended helper, so it never shows as a top-level row", () => {
    const stopped = helper("working", { state: { ...lead.state, running: false } })
    expect(ids(treeSessions([lead, stopped]))).toEqual(["p"])
    expect(ids(treeSessions([stopped]))).toEqual([])
    expect(ids(treeSessions([lead, helper("starting", { ended_at: "2026-01-01T00:00:00Z" })]))).toEqual(["p"])
  })

  it("keeps a stopped helper while its parent still waits on its reply", () => {
    const stopped = helper("idle", { ended_at: "2026-01-01T00:00:00Z", state: { ...lead.state, running: false } })
    expect(ids(treeSessions([{ ...lead, waiting: [{ kind: "reply", peer: "h" }] }, stopped]))).toEqual(["p", "h"])
  })

  it("keeps an idle helper only while the parent waits on its reply or it has background work", () => {
    expect(ids(treeSessions([lead, helper("idle")]))).toEqual(["p"])
    expect(ids(treeSessions([{ ...lead, waiting: [{ kind: "reply", peer: "h" }] }, helper("idle")]))).toEqual(["p", "h"])
    expect(ids(treeSessions([lead, helper("idle", { waiting: [{ kind: "agent" }] })]))).toEqual(["p", "h"])
  })

  it("keeps running subagents and drops finished ones", () => {
    const chat = { ...lead, subagents: [{ id: "a", state: "running" }, { id: "b", state: "idle" }, { id: "c", state: "requires_action" }] }
    expect(treeSessions([chat])[0]?.subagents?.map((sub) => sub.id)).toEqual(["a", "c"])
  })
})
