export type IssueStatus = "open" | "resolved" | "ignored"
export type IssueFilter = IssueStatus | "all"

export type DiagnosticIssue = {
  fingerprint: string
  kind: string
  title: string
  status: IssueStatus
  regressed: boolean
  count: number
  first_seen: string
  last_seen: string
  resolved_version?: string
  note?: string
  counts: { node_id: string; count: number; last_version: string; last_seen: string }[]
  sample?: Record<string, unknown>
  recent_traces?: string[]
}

export const issueFilters = ["open", "resolved", "ignored", "all"] as const
export const issueLabels = { open: "Open", resolved: "Resolved", ignored: "Ignored", all: "All" }

export function issueStatus(issue: Pick<DiagnosticIssue, "status" | "regressed">) {
  return issue.status === "open" && issue.regressed ? "Regressed" : issueLabels[issue.status]
}

export function resolutionError(version: string) {
  return version.trim() ? "" : "Enter the version containing the fix."
}
