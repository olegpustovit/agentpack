import { describe, expect, it } from "vitest"
import { groupSessions, type ListedSession } from "./group"

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
