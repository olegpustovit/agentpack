import { useState, type ReactNode } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { GitPullRequest } from "lucide-react"
import { Section } from "@/components/kit/data"
import { FieldError } from "@/components/kit/forms"
import { ConfirmAlert, ConfirmAlertContent, Sheet, SheetActions, SheetContent, SheetDescription, SheetTitle } from "@/components/kit/overlays"
import { Button, buttonVariants } from "@/components/ui/button"
import { api, ApiError, errorText, routeMissing } from "@/lib/api"
import type { SessionInfo } from "@/lib/api-types"
import { plural } from "@/lib/format"
import { hud, useCopied } from "@/lib/hud"
import { pullRequestStoragePrefix } from "@/lib/persist"
import { sessionGitQuery, type SessionGit } from "@/features/sessions/git"

const gitPath = (sessionId: string, action: string): `/api/${string}` => `/api/sessions/${encodeURIComponent(sessionId)}/git/${action}`

// The session's branch, how far it is from its base, and its pull request.
export function useSessionGit(machine: string, sessionId: string, info?: SessionInfo) {
  const summary = useQuery({ ...sessionGitQuery(machine, sessionId), enabled: Boolean(info?.directory) })
  const details = useQuery({ ...sessionGitQuery(machine, sessionId, true), enabled: false, retry: false })
  return { git: summary.data?.deferred ? details.data : summary.data, canLoad: Boolean(summary.data?.deferred), details }
}

// "ap/replay-offset · 2 ahead of main", for the toolbar subtitle.
export function branchLine(git?: SessionGit): string | undefined {
  if (git?.removed) return "Worktree removed"
  if (!git?.available || !git.branch) return undefined
  const ahead = git.base && git.ahead > 0 ? ` · ${git.ahead} ahead of ${git.base}` : ""
  return `⑂ ${git.branch}${ahead}`
}

function useGitRefresh(machine: string, sessionId: string) {
  const queryClient = useQueryClient()
  return () => {
    void queryClient.invalidateQueries({ queryKey: sessionGitQuery(machine, sessionId).queryKey })
    const details = sessionGitQuery(machine, sessionId, true)
    if (queryClient.getQueryData(details.queryKey)) {
      void queryClient.prefetchQuery({ ...details, staleTime: 0, retry: false })
    }
  }
}

type PullRequestLink = { url: string; created: boolean }

function pullRequestLink(raw: { url?: string; created: boolean } | null): PullRequestLink | undefined {
  if (raw?.url && /^https?:\/\//i.test(raw.url)) return { url: raw.url, created: raw.created }
}

export function usePullRequest(machine: string, sessionId: string, branch?: string) {
  const queryClient = useQueryClient()
  const queryKey = ["pull-request-link", machine, sessionId, branch] as const
  const storageKey = `${pullRequestStoragePrefix}${JSON.stringify(queryKey)}`
  const { data: link } = useQuery<PullRequestLink>({
    queryKey,
    enabled: false,
    initialData: () => {
      if (!branch) return undefined
      try {
        return pullRequestLink(JSON.parse(String(localStorage.getItem(storageKey))))
      } catch {
        return undefined
      }
    },
  })
  const refresh = useGitRefresh(machine, sessionId)
  const pr = useMutation({
    mutationFn: () => api<{ url?: string; created: boolean }>(gitPath(sessionId, "pr"), { machine, method: "POST", body: {} }),
    onSuccess: (result) => {
      const next = pullRequestLink(result)
      if (next) {
        queryClient.setQueryData(queryKey, next)
        try {
          localStorage.setItem(storageKey, JSON.stringify(next))
        } catch {
          // The link still works while this tab is open.
        }
      }
      refresh()
      hud.success(result.created ? "Pull request created" : "Branch pushed")
    },
    onError: (error) => hud.error(errorText(error, "Couldn't create the pull request."), routeMissing(error) ? undefined : () => pr.mutate()),
  })
  return { ...pr, link }
}

export function PullRequestConfirmation({ branch, onConfirm }: { branch: string; onConfirm: () => void }) {
  return (
    <ConfirmAlertContent
      title="Create pull request?"
      description={`Pushes branch ${branch} to origin. Once pushed, you can open the pull request or finish creating it on GitHub.`}
      destructive={false}
      destructiveLabel="Create pull request"
      onConfirm={onConfirm}
    />
  )
}

function useGitActions(machine: string, sessionId: string) {
  const refresh = useGitRefresh(machine, sessionId)
  const commit = useMutation({
    mutationFn: (message: string) => api<{ commit: string }>(gitPath(sessionId, "commit"), { machine, method: "POST", body: { message } }),
    onSuccess: () => {
      hud.success("Committed")
      refresh()
    },
  })
  const merge = useMutation({
    mutationFn: () => api<{ merged: boolean }>(gitPath(sessionId, "merge"), { machine, method: "POST", body: {} }),
    onSuccess: () => {
      hud.success("Merged into the main folder")
      refresh()
    },
  })
  return { commit, merge }
}

// Commit… and Create pull request, for sessions in their own worktree.
export function GitToolbarActions({ machine, sessionId, info, git, readOnly, inDetails = false }: { machine: string; sessionId: string; info?: SessionInfo; git?: SessionGit; readOnly?: boolean; inDetails?: boolean }) {
  const [committing, setCommitting] = useState(false)
  const [confirmPR, setConfirmPR] = useState(false)
  const actions = useGitActions(machine, sessionId)
  const pr = usePullRequest(machine, sessionId, git?.branch)
  if (!git?.worktree || !git.available) return null
  const changed = git.dirty + git.untracked
  const openPR = git.pr?.state === "OPEN" ? git.pr : undefined
  const link = openPR?.url ?? pr.link?.url
  return (
    <>
      <Button type="button" size="sm" disabled={readOnly || changed === 0} aria-label={inDetails ? "Commit from chat details" : undefined} title={changed === 0 ? "Nothing to commit" : `Commit ${plural(changed, "changed file")}`} onClick={() => setCommitting(true)}>
        Commit…
      </Button>
      {link ? (
        <a href={link} target="_blank" rel="noreferrer" className={buttonVariants({ size: "sm" })} aria-label={inDetails ? "Open branch review from chat details" : undefined}>
          <GitPullRequest aria-hidden />
          {openPR ? `Pull request #${openPR.number}` : pr.link?.created ? "Open pull request" : "Open GitHub compare"}
        </a>
      ) : (
        <Button type="button" size="sm" variant="primary"
          disabled={readOnly || pr.isPending || !git.branch || git.ahead === 0}
          aria-label={inDetails ? "Create pull request from chat details" : undefined}
          title={git.ahead === 0 ? "Commit something first" : "Review push before creating a pull request"}
          onClick={() => setConfirmPR(true)}
        >
          <GitPullRequest aria-hidden />
          {pr.isPending ? "Pushing…" : "Create pull request"}
        </Button>
      )}
      <ConfirmAlert open={confirmPR} onOpenChange={setConfirmPR}>
        {confirmPR && <PullRequestConfirmation branch={git.branch} onConfirm={() => { setConfirmPR(false); pr.mutate() }} />}
      </ConfirmAlert>
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
        {commit.error && <FieldError>{errorText(commit.error, "Couldn't commit.")}</FieldError>}
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
  info,
  git,
  readOnly,
  onAskRebase,
}: {
  machine: string
  sessionId: string
  info?: SessionInfo
  git?: SessionGit
  readOnly?: boolean
  onAskRebase: (text: string) => void
}) {
  const actions = useGitActions(machine, sessionId)
  const pr = usePullRequest(machine, sessionId, git?.branch)
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
              </div>
              <h3 className="mt-(--s4) text-(length:--fs-xs) font-semibold text-(--fg2)">Write</h3>
              <div className="mt-(--s2) flex flex-wrap gap-(--s2)">
                <GitToolbarActions machine={machine} sessionId={sessionId} info={info} git={git} readOnly={readOnly} inDetails />
                <Button type="button" size="sm" disabled={readOnly || git.ahead === 0 || actions.merge.isPending} title="Merge with --no-ff into the main folder's branch" onClick={() => setMerging(true)}>
                  Merge into {git.base || "main"}…
                </Button>
              </div>
              {actions.merge.error && (
                <div className="mt-(--s2) flex flex-col gap-(--s2)">
                  <FieldError>{errorText(actions.merge.error, "Couldn't merge.")}</FieldError>
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
                    destructive={false}
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
              ) : pr.link ? (
                <a href={pr.link.url} target="_blank" rel="noreferrer" className="text-(--accent) hover:underline" aria-label="Open saved branch review link">
                  {pr.link.created ? "Open pull request" : "Open GitHub compare"}
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
