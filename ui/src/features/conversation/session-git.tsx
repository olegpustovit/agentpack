import { useState, type ReactNode } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { GitPullRequest } from "lucide-react"
import { Section } from "@/components/kit/data"
import { FieldError } from "@/components/kit/forms"
import { ConfirmAlert, ConfirmAlertContent, Sheet, SheetActions, SheetContent, SheetDescription, SheetTitle } from "@/components/kit/overlays"
import { Button } from "@/components/ui/button"
import { ApiError, api, routeMissing } from "@/lib/api"
import type { SessionInfo } from "@/lib/api-types"
import { plural } from "@/lib/format"
import { hud, useCopied } from "@/lib/hud"
import { sessionGitQuery, type SessionGit } from "@/features/sessions/git"

const gitPath = (sessionId: string, action: string): `/api/${string}` => `/api/sessions/${encodeURIComponent(sessionId)}/git/${action}`

function failure(error: unknown, fallback: string): string {
  if (routeMissing(error)) return "Needs update"
  return error instanceof Error ? error.message : fallback
}

// The session's branch, how far it is from its base, and its pull request.
export function useSessionGit(machine: string, sessionId: string, info?: SessionInfo) {
  return useQuery({ ...sessionGitQuery(machine, sessionId), enabled: Boolean(info?.directory) })
}

// "ap/replay-offset · 2 ahead of main", for the toolbar subtitle.
export function branchLine(git?: SessionGit): string | undefined {
  if (git?.removed) return "Worktree removed"
  if (!git?.available || !git.branch) return undefined
  const ahead = git.base && git.ahead > 0 ? ` · ${git.ahead} ahead of ${git.base}` : ""
  return `⑂ ${git.branch}${ahead}`
}

function useGitActions(machine: string, sessionId: string) {
  const queryClient = useQueryClient()
  const refresh = () => void queryClient.invalidateQueries({ queryKey: ["m", machine, "session-git", sessionId] })
  const commit = useMutation({
    mutationFn: (message: string) => api<{ commit: string }>(gitPath(sessionId, "commit"), { machine, method: "POST", body: { message } }),
    onSuccess: () => {
      hud.success("Committed")
      refresh()
    },
  })
  const pr = useMutation({
    mutationFn: () => api<{ url?: string; created: boolean }>(gitPath(sessionId, "pr"), { machine, method: "POST", body: {} }),
    onSuccess: (result) => {
      refresh()
      if (result.created) hud.success("Pull request created")
      // Without gh the gateway pushes and returns GitHub's compare page to finish there.
      if (result.url) window.open(result.url, "_blank", "noopener,noreferrer")
      else if (!result.created) hud.success("Branch pushed")
    },
    onError: (error) => hud.error(failure(error, "Couldn't create the pull request."), routeMissing(error) ? undefined : () => pr.mutate()),
  })
  const merge = useMutation({
    mutationFn: () => api<{ merged: boolean }>(gitPath(sessionId, "merge"), { machine, method: "POST", body: {} }),
    onSuccess: () => {
      hud.success("Merged into the main folder")
      refresh()
    },
  })
  return { commit, pr, merge }
}

// Commit… and Create pull request, for sessions in their own worktree.
export function GitToolbarActions({ machine, sessionId, info, git, readOnly }: { machine: string; sessionId: string; info?: SessionInfo; git?: SessionGit; readOnly?: boolean }) {
  const [committing, setCommitting] = useState(false)
  const actions = useGitActions(machine, sessionId)
  if (!git?.worktree || !git.available) return null
  const changed = git.dirty + git.untracked
  return (
    <>
      <Button type="button" size="sm" disabled={readOnly || changed === 0} title={changed === 0 ? "Nothing to commit" : `Commit ${plural(changed, "changed file")}`} onClick={() => setCommitting(true)}>
        Commit…
      </Button>
      <Button
        type="button"
        size="sm"
        variant="primary"
        disabled={readOnly || actions.pr.isPending || (git.ahead === 0 && !git.pr)}
        title={git.ahead === 0 ? "Commit something first" : git.gh ? "Push the branch and open a pull request" : "Push the branch and open GitHub's compare page"}
        onClick={() => (git.pr?.state === "OPEN" ? window.open(git.pr.url, "_blank", "noopener,noreferrer") : actions.pr.mutate())}
      >
        <GitPullRequest aria-hidden />
        {git.pr?.state === "OPEN" ? `Pull request #${git.pr.number}` : actions.pr.isPending ? "Pushing…" : "Create pull request"}
      </Button>
      <Sheet open={committing} onOpenChange={setCommitting}>
        {committing && <CommitSheet initial={info?.name || info?.title || ""} changed={changed} branch={git.branch} commit={actions.commit} onDone={() => setCommitting(false)} />}
      </Sheet>
    </>
  )
}

function CommitSheet({ initial, changed, branch, commit, onDone }: { initial: string; changed: number; branch: string; commit: ReturnType<typeof useGitActions>["commit"]; onDone: () => void }) {
  const [message, setMessage] = useState(initial)
  return (
    <SheetContent>
      <form
        className="contents"
        onSubmit={(event) => {
          event.preventDefault()
          commit.mutate(message, { onSuccess: onDone })
        }}
      >
        <SheetTitle>Commit {plural(changed, "file")}</SheetTitle>
        <SheetDescription>Stages every change in the worktree and commits it on {branch}.</SheetDescription>
        <textarea
          aria-label="Commit message"
          value={message}
          onChange={(event) => setMessage(event.target.value)}
          rows={4}
          autoFocus
          className="w-full resize-y rounded-(--r-ctl) border-[0.5px] border-(--sep) bg-(--field) px-(--s3) py-(--s2) text-(length:--fs) outline-none focus:ring-2 focus:ring-(--accent-soft)"
        />
        {commit.error && <FieldError>{failure(commit.error, "Couldn't commit.")}</FieldError>}
        <SheetActions>
          <Button type="submit" variant="primary" disabled={!message.trim() || commit.isPending}>
            {commit.isPending ? "Committing…" : "Commit"}
          </Button>
        </SheetActions>
      </form>
    </SheetContent>
  )
}

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex min-w-0 items-baseline justify-between gap-(--s4)">
      <dt className="shrink-0 text-(--fg2)">{label}</dt>
      <dd className="min-w-0 truncate text-right" title={typeof children === "string" ? children : undefined}>{children}</dd>
    </div>
  )
}

function checksWords(git: SessionGit): string {
  const checks = git.pr?.checks
  if (!git.pr) return git.gh ? "Run after the PR opens" : "Install gh to see checks"
  if (!checks || checks.total === 0) return "None"
  if (checks.failed > 0) return `${checks.failed} failed of ${checks.total}`
  if (checks.pending > 0) return `${checks.pending} running of ${checks.total}`
  return `All ${checks.total} passed`
}

// Worktree and Pull request sections of the inspector.
export function GitInspectorSections({
  machine,
  sessionId,
  git,
  readOnly,
  onAskRebase,
}: {
  machine: string
  sessionId: string
  git?: SessionGit
  readOnly?: boolean
  onAskRebase: (text: string) => void
}) {
  const actions = useGitActions(machine, sessionId)
  const [merging, setMerging] = useState(false)
  const [pathCopied, copyPath] = useCopied()
  if (!git || (!git.available && !git.removed)) return null
  const conflict = actions.merge.error instanceof ApiError && actions.merge.error.status === 409 && /conflict/i.test(actions.merge.error.message)
  return (
    <>
      {git.worktree && (
        <Section title="Worktree">
          {git.removed ? (
            <p className="text-(--fg2)">Worktree removed. The chat stays readable.</p>
          ) : (
            <>
              <dl className="flex flex-col gap-(--s2)">
                <Row label="Branch">{git.branch || "detached"}</Row>
                <Row label="Changes">{git.dirty + git.untracked === 0 ? "None" : plural(git.dirty + git.untracked, "file")}</Row>
                {git.base && <Row label="Base">{git.merged && git.ahead === 0 ? `${git.base}, merged` : `${git.base}${git.behind > 0 ? `, ${git.behind} behind` : ""}`}</Row>}
              </dl>
              <p className="mt-(--s2) text-(length:--fs-xs) text-(--fg3)" title="Packages and modes come from the main folder; this worktree's agentpack.toml applies after it is merged.">
                Packages load from the main folder.
              </p>
              <div className="mt-(--s3) flex flex-wrap gap-(--s2)">
                {git.path && (
                  <Button type="button" size="sm" onClick={() => copyPath(git.path ?? "")}>
                    {pathCopied ? "Copied" : "Copy path"}
                  </Button>
                )}
                <Button type="button" size="sm" disabled={readOnly || git.ahead === 0 || actions.merge.isPending} title="Merge with --no-ff into the main folder's branch" onClick={() => setMerging(true)}>
                  Merge into {git.base || "main"}…
                </Button>
              </div>
              {actions.merge.error && (
                <div className="mt-(--s2) flex flex-col gap-(--s2)">
                  <FieldError>{failure(actions.merge.error, "Couldn't merge.")}</FieldError>
                  {conflict && !readOnly && (
                    <Button type="button" size="sm" className="self-start" onClick={() => onAskRebase(`Rebase this branch (${git.branch}) onto ${git.base || "the main folder's branch"} and resolve the conflicts, then commit.`)}>
                      Ask the agent to rebase
                    </Button>
                  )}
                </div>
              )}
              <ConfirmAlert open={merging} onOpenChange={setMerging}>
                {merging && (
                  <ConfirmAlertContent
                    title={`Merge ${git.branch} into ${git.base || "main"}?`}
                    description={`Creates a merge commit in the main folder with ${plural(git.ahead, "commit")}. Refused if the main folder has uncommitted changes.`}
                    destructiveLabel="Merge"
                    onConfirm={() => {
                      setMerging(false)
                      actions.merge.mutate()
                    }}
                  />
                )}
              </ConfirmAlert>
            </>
          )}
        </Section>
      )}
      {git.available && git.branch && (
        <Section title="Pull request">
          <dl className="flex flex-col gap-(--s2)">
            <Row label="Branch">{git.branch}</Row>
            <Row label="State">
              {git.pr ? (
                <a href={git.pr.url} target="_blank" rel="noreferrer" className="text-(--accent) hover:underline">
                  #{git.pr.number} {git.pr.state.toLowerCase()}
                </a>
              ) : git.ahead > 0 ? (
                `Not opened · ${plural(git.ahead, "commit")} ahead`
              ) : (
                "Not opened"
              )}
            </Row>
            <Row label="Checks">{checksWords(git)}</Row>
          </dl>
        </Section>
      )}
    </>
  )
}
