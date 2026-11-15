import { useState } from "react"
import { queryOptions, useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { KeyValue, Section } from "@/components/kit/data"
import { Empty, RowSkeleton } from "@/components/kit/feedback"
import { Field, FieldError, FieldGrid, FieldLabel } from "@/components/kit/forms"
import { SegmentedControl, SegmentedControlItem, SourceList, SourceRow } from "@/components/kit/list"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { api, LOCAL } from "@/lib/api"
import { plural, relativeTime } from "@/lib/format"
import { hud } from "@/lib/hud"
import { issueFilters, issueLabels, issueStatus, resolutionError, type DiagnosticIssue, type IssueFilter } from "./diagnostics-model"
import { LoadError, Pane } from "./panes"

const issuesQuery = (status: IssueFilter) => queryOptions({
  queryKey: ["diagnostics", "issues", status],
  queryFn: () => api<DiagnosticIssue[]>("/api/logs/issues", { query: { status: status === "all" ? "" : status } }),
  refetchInterval: 30_000,
})

const issueQuery = (fingerprint: string) => queryOptions({
  queryKey: ["diagnostics", "issue", fingerprint],
  queryFn: () => api<DiagnosticIssue>(`/api/logs/issues/${encodeURIComponent(fingerprint)}`),
  refetchInterval: 30_000,
})

export function DiagnosticsPane() {
  const [status, setStatus] = useState<IssueFilter>("open")
  const [selected, setSelected] = useState("")
  return (
    <Pane title="Diagnostics">
      {selected ? (
        <IssueDetail key={selected} fingerprint={selected} back={() => setSelected("")} />
      ) : (
        <div className="flex min-w-0 flex-col gap-(--s4)">
          <SegmentedControl aria-label="Issue status" value={[status]} onValueChange={(values) => {
            const next = issueFilters.find((item) => item === values[0])
            if (next) setStatus(next)
          }}>
            {issueFilters.map((item) => <SegmentedControlItem key={item} value={item}>{issueLabels[item]}</SegmentedControlItem>)}
          </SegmentedControl>
          <IssueList status={status} select={setSelected} />
        </div>
      )}
    </Pane>
  )
}

function IssueList({ status, select }: { status: IssueFilter; select: (fingerprint: string) => void }) {
  const issues = useQuery(issuesQuery(status))
  if (issues.error) return <LoadError what="diagnostic issues" error={issues.error} retry={() => void issues.refetch()} />
  if (!issues.data) return <RowSkeleton lines={2} />
  if (!issues.data.length) return <Empty title={status === "all" ? "No recorded issues" : `No ${status} issues`}>Recorded failures appear here. Choose another status to see earlier issues.</Empty>
  return (
    <SourceList aria-label="Diagnostic issues">
      {issues.data.map((issue) => (
        <SourceRow key={issue.fingerprint} onClick={() => select(issue.fingerprint)} className="h-auto min-h-(--row-h-2) flex-col items-stretch gap-(--s1) py-(--s2)">
          <span className="truncate" title={issue.title}>{issue.title}</span>
          <span className="truncate text-(length:--fs-sm) text-(--fg2)">
            {issueStatus(issue)} · {plural(issue.count, "occurrence")} · {plural(issue.counts.length, "machine")} · {relativeTime(new Date(issue.last_seen))}
          </span>
        </SourceRow>
      ))}
    </SourceList>
  )
}

type IssueChange = { action: "resolve" | "reopen" | "ignore"; version?: string; note?: string }

function IssueDetail({ fingerprint, back }: { fingerprint: string; back: () => void }) {
  const queryClient = useQueryClient()
  const result = useQuery(issueQuery(fingerprint))
  const [resolving, setResolving] = useState(false)
  const [version, setVersion] = useState("")
  const [note, setNote] = useState("")
  const [validation, setValidation] = useState("")
  const update = useMutation({
    mutationFn: ({ action, ...body }: IssueChange) => api<DiagnosticIssue>(`/api/logs/issues/${encodeURIComponent(fingerprint)}/${action}`, { machine: LOCAL, body }),
    onSuccess: (issue, { action }) => {
      queryClient.setQueryData(issueQuery(fingerprint).queryKey, issue)
      void queryClient.invalidateQueries({ queryKey: ["diagnostics", "issues"] })
      setResolving(false)
      setValidation("")
      hud.success({ resolve: "Issue resolved.", reopen: "Issue reopened.", ignore: "Issue ignored." }[action])
    },
    onError: (error, input) => {
      if (input.action !== "resolve") hud.error(`Couldn't ${input.action} the issue: ${error.message}`, () => update.mutate(input))
    },
  })
  const issue = result.data
  const error = validation || update.error?.message
  return (
    <div className="flex min-w-0 flex-col gap-(--s5)">
      <div><Button variant="ghost" autoFocus onClick={back}>Back to issues</Button></div>
      {result.error ? <LoadError what="the issue" error={result.error} retry={() => void result.refetch()} /> : !issue ? <RowSkeleton lines={2} /> : <>
        <h3 className="break-words font-semibold">{issue.title}</h3>
        <dl className="flex min-w-0 flex-col gap-(--s3)">
          <KeyValue label="Status">{issueStatus(issue)}</KeyValue>
          <KeyValue label="Occurrences">{issue.count.toLocaleString()}</KeyValue>
          <KeyValue label="First seen"><time dateTime={issue.first_seen}>{new Date(issue.first_seen).toLocaleString()}</time></KeyValue>
          <KeyValue label="Last seen"><time dateTime={issue.last_seen}>{new Date(issue.last_seen).toLocaleString()}</time></KeyValue>
          <KeyValue label="Fingerprint"><code className="break-all">{issue.fingerprint}</code></KeyValue>
          {issue.resolved_version && <KeyValue label="Fix version">{issue.resolved_version}</KeyValue>}
          {issue.note && <KeyValue label="Note">{issue.note}</KeyValue>}
        </dl>
        {resolving ? (
          <form onSubmit={(event) => {
            event.preventDefault()
            const message = resolutionError(version)
            setValidation(message)
            if (!message) update.mutate({ action: "resolve", version: version.trim(), note: note.trim() })
          }}>
            <FieldGrid>
              <Field data-invalid={validation ? "true" : undefined}>
                <FieldLabel htmlFor="fix-version">Fix version</FieldLabel>
                <Input id="fix-version" value={version} onChange={(event) => { setVersion(event.target.value); setValidation("") }} placeholder="v0.14.2" maxLength={256} autoFocus aria-invalid={!!validation} aria-describedby={error ? "resolution-help resolution-error" : "resolution-help"} disabled={update.isPending} />
              </Field>
              <p id="resolution-help" className="text-(length:--fs-sm) text-(--fg2)">A new occurrence at or after this version reopens the issue.</p>
              <Field>
                <FieldLabel htmlFor="fix-note">Note (optional)</FieldLabel>
                <Input id="fix-note" value={note} onChange={(event) => setNote(event.target.value)} maxLength={4096} disabled={update.isPending} />
              </Field>
              {error && <FieldError id="resolution-error">{error}</FieldError>}
              <div className="flex justify-end gap-(--s3)">
                <Button type="button" variant="ghost" disabled={update.isPending} onClick={() => setResolving(false)}>Cancel</Button>
                <Button type="submit" disabled={update.isPending}>{update.isPending ? "Resolving…" : "Resolve issue"}</Button>
              </div>
            </FieldGrid>
          </form>
        ) : (
          <div className="flex flex-wrap gap-(--s3)">
            {issue.status !== "resolved" && <Button disabled={update.isPending} onClick={() => { update.reset(); setResolving(true) }}>Resolve…</Button>}
            {issue.status !== "open" && <Button disabled={update.isPending} onClick={() => update.mutate({ action: "reopen" })}>Reopen</Button>}
            {issue.status !== "ignored" && <Button variant="ghost" disabled={update.isPending} onClick={() => update.mutate({ action: "ignore" })}>Ignore</Button>}
          </div>
        )}
        <Section title="Machines">
          <dl className="flex min-w-0 flex-col gap-(--s3)">
            {issue.counts.map((count) => <KeyValue key={count.node_id} label="Machine"><code className="break-all">{count.node_id}</code> · {plural(count.count, "occurrence")} · {count.last_version || "Version unknown"}</KeyValue>)}
          </dl>
        </Section>
        {issue.sample && <Section title="Sample event"><pre className="max-w-full overflow-auto whitespace-pre-wrap break-words text-(length:--fs-sm)">{JSON.stringify(issue.sample, null, 2)}</pre></Section>}
        {!!issue.recent_traces?.length && <Section title="Recent traces"><ul className="flex flex-col gap-(--s2)">{issue.recent_traces.map((trace) => <li key={trace}><code className="break-all">{trace}</code></li>)}</ul></Section>}
      </>}
    </div>
  )
}
