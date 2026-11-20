import { describe, expect, it } from "vitest"
import { branchSlug, isWorktreePath, parseSessionGit, projectDirectory, worktreeFolderInProject, worktreeState, type WorktreeRow } from "./git"

const row = (patch: Partial<WorktreeRow>): WorktreeRow => ({ path: "/h/worktrees/0123456789abcdef/x", main: "/work/app", branch: "ap/x", head: "abc", dirty: 0, untracked: 0, unpushed: 0, merged: false, orphaned: false, sessions: [], running: 0, size: 0, ...patch })

describe("worktrees", () => {
  it("names what is left in a worktree", () => {
    expect(worktreeState(row({ dirty: 2, untracked: 1, unpushed: 2 }))).toBe("3 uncommitted files · 2 commits not pushed")
    expect(worktreeState(row({ merged: true }))).toBe("Merged")
    expect(worktreeState(row({ orphaned: true }))).toBe("Main folder is gone")
  })

  it("matches the gateway's branch slug", () => {
    expect(branchSlug("Fix the overlay cleanup!")).toBe("fix-the-overlay-cleanup")
    expect(branchSlug("")).toBe("session-…")
  })

  it("puts a worktree session under its main folder", () => {
    const directory = "/home/me/.local/share/agentpack/worktrees/0123456789abcdef/x/web"
    expect(isWorktreePath(directory)).toBe(true)
    expect(projectDirectory({ directory, definition_root: "/work/app" })).toBe("/work/app")
    expect(projectDirectory({ directory, definition_root: "/work", worktree_of: "/work/app/web" })).toBe("/work/app/web")
    expect(projectDirectory({ directory: "/work/app/web", definition_root: "/work/app" })).toBe("/work/app/web")
    expect(worktreeFolderInProject(row({}), "/work/app/web")).toBe(true)
    expect(worktreeFolderInProject(row({}), "/work/application")).toBe(false)
  })

  it("reads a git state from a peer that omits newer fields", () => {
    expect(parseSessionGit({ available: true, branch: "main" })).toMatchObject({ available: true, branch: "main", ahead: 0, added: 0, worktree: false, pr: undefined })
    expect(parseSessionGit({ available: true, pr: { number: 7, url: "u", state: "OPEN", checks: { total: 2, passed: 1, failed: 0, pending: 1 } } }).pr?.checks.pending).toBe(1)
  })
})
