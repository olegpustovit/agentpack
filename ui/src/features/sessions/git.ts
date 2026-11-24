import { queryOptions } from "@tanstack/react-query"
import { ApiError, api } from "@/lib/api"
import { array, boolean, number, object, optional, string } from "@/lib/validate"
import { plural } from "@/lib/format"

// Worktrees and Git state come from the machine running the agent. A peer without these routes
// answers 404, which reads as Needs update.

export type PullRequest = { number: number; url: string; state: string; checks: { total: number; passed: number; failed: number; pending: number } }

export type SessionGit = {
  available: boolean
  deferred?: boolean
  removed: boolean
  branch: string
  head: string
  base?: string
  upstream?: string
  ahead: number
  behind: number
  dirty: number
  untracked: number
  worktree: boolean
  path?: string
  merged: boolean
  added: number
  deleted: number
  gh: boolean
  pr?: PullRequest
}

export function parseSessionGit(raw: unknown): SessionGit {
  const body = object(raw, "git")
  const count = (key: string) => optional(body[key], key, number) ?? 0
  const pr = body.pr === undefined || body.pr === null ? undefined : object(body.pr, "pr")
  const checks = pr ? object(pr.checks, "pr.checks") : undefined
  return {
    available: boolean(body.available, "available"),
    deferred: optional(body.deferred, "deferred", boolean),
    removed: optional(body.removed, "removed", boolean) ?? false,
    branch: optional(body.branch, "branch", string) ?? "",
    head: optional(body.head, "head", string) ?? "",
    base: optional(body.base, "base", string),
    upstream: optional(body.upstream, "upstream", string),
    ahead: count("ahead"),
    behind: count("behind"),
    dirty: count("dirty"),
    untracked: count("untracked"),
    worktree: optional(body.worktree, "worktree", boolean) ?? false,
    path: optional(body.path, "path", string),
    merged: optional(body.merged, "merged", boolean) ?? false,
    added: count("added"),
    deleted: count("deleted"),
    gh: optional(body.gh, "gh", boolean) ?? false,
    pr:
      pr && checks
        ? {
            number: number(pr.number, "pr.number"),
            url: string(pr.url, "pr.url"),
            state: string(pr.state, "pr.state"),
            checks: { total: number(checks.total, "total"), passed: number(checks.passed, "passed"), failed: number(checks.failed, "failed"), pending: number(checks.pending, "pending") },
          }
        : undefined,
  }
}

const noRetryOn404 = (count: number, error: Error) => !(error instanceof ApiError && error.status === 404) && count < 2

export const sessionGitQuery = (machine: string, sessionId: string, inspect = false) =>
  queryOptions({
    queryKey: ["m", machine, "session-git", sessionId, inspect],
    queryFn: async () => parseSessionGit(await api<unknown>(`/api/sessions/${encodeURIComponent(sessionId)}/git`, { machine, query: inspect ? { inspect: "1" } : undefined })),
    staleTime: 30 * 1000,
    retry: noRetryOn404,
  })

export type WorktreeRow = {
  path: string
  main?: string
  branch: string
  head: string
  base?: string
  upstream?: string
  dirty: number
  untracked: number
  unpushed: number
  merged: boolean
  orphaned: boolean
  sessions: string[]
  running: number
  size: number
  error?: string
}

export type Worktrees = { git: boolean; worktrees: WorktreeRow[] }

export function parseWorktrees(raw: unknown): Worktrees {
  const body = object(raw, "worktrees")
  return {
    git: boolean(body.git, "git"),
    worktrees: array(body.worktrees, "worktrees").map((item, index) => {
      const row = object(item, `worktrees[${index}]`)
      const at = (key: string) => `worktrees[${index}].${key}`
      const count = (key: string) => optional(row[key], at(key), number) ?? 0
      return {
        path: string(row.path, at("path")),
        main: optional(row.main, at("main"), string),
        branch: optional(row.branch, at("branch"), string) ?? "",
        head: optional(row.head, at("head"), string) ?? "",
        base: optional(row.base, at("base"), string),
        upstream: optional(row.upstream, at("upstream"), string),
        dirty: count("dirty"),
        untracked: count("untracked"),
        unpushed: count("unpushed"),
        merged: optional(row.merged, at("merged"), boolean) ?? false,
        orphaned: optional(row.orphaned, at("orphaned"), boolean) ?? false,
        sessions: array(row.sessions ?? [], at("sessions")).map((id, i) => string(id, `${at("sessions")}[${i}]`)),
        running: count("running"),
        size: count("size"),
        error: optional(row.error, at("error"), string),
      }
    }),
  }
}

export const worktreesQuery = (machine: string, directory = "") =>
  queryOptions({
    queryKey: ["m", machine, "worktrees", ...(directory ? [directory] : [])],
    queryFn: async () => parseWorktrees(await api<unknown>("/api/worktrees", { machine, query: directory ? { directory } : undefined })),
    staleTime: 30 * 1000,
    refetchOnWindowFocus: !directory,
    refetchOnReconnect: !directory,
    retry: noRetryOn404,
  })

// Worktrees live in $AGENTPACK_HOME/worktrees/<16 hex>/<slug>; the main folder is the session's definition root.
const WORKTREE_PATH = /[\\/]worktrees[\\/][0-9a-f]{16}[\\/][^\\/]+(?:[\\/]|$)/

export function isWorktreePath(directory: string): boolean {
  return WORKTREE_PATH.test(directory)
}

// The folder a session belongs to as a project: a worktree session counts under its main folder,
// which the gateway records. Rows from older peers fall back to the worktree's definition root.
export function projectDirectory(session: { directory: string; definition_root?: string; worktree_of?: string }): string {
  if (session.worktree_of) return session.worktree_of
  return session.definition_root && isWorktreePath(session.directory) ? session.definition_root : session.directory
}

export function worktreeFolderInProject(worktree: WorktreeRow, directory: string): boolean {
  const main = worktree.main?.replace(/[\\/]+$/, "")
  const folder = directory.replace(/[\\/]+$/, "")
  return Boolean(main) && (folder === main || folder.startsWith(`${main}/`) || folder.startsWith(`${main}\\`))
}

// What is still in a worktree, in the words the Machine page shows.
export function worktreeState(row: WorktreeRow): string {
  if (row.orphaned) return "Main folder is gone"
  if (row.error) return row.error
  const parts: string[] = []
  const changed = row.dirty + row.untracked
  if (changed > 0) parts.push(`${plural(changed, "uncommitted file")}`)
  if (row.unpushed > 0 && !row.merged) parts.push(`${plural(row.unpushed, "commit")} not pushed`)
  if (parts.length === 0) parts.push(row.merged ? "Merged" : "Clean")
  return parts.join(" · ")
}

// The branch name the gateway makes from a first message (worktreeSlug in worktree.go).
export function branchSlug(prompt: string): string {
  const words = prompt.toLowerCase().split(/\s+/).filter(Boolean).slice(0, 6)
  let slug = words.join("-").replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "")
  if (slug.length > 40) slug = slug.slice(0, 40).replace(/-+$/, "")
  return slug || "session-…"
}
