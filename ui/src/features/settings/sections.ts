// Settings sections without their panes, so the System sidebar can list them without loading any.
import { useQuery } from "@tanstack/react-query"
import { Activity, Bell, Bot, Sparkles, Globe, KeyRound, Palette, RefreshCw, Server, Users, type LucideIcon } from "lucide-react"
import { useEventsConnected } from "@/features/events/use-events"
import { replicationQuery } from "@/features/library/queries"
import { connectionsQuery } from "./connections-model"
import { issuesQuery } from "./diagnostics-model"
import { sharedSignInsQuery, vaultBadge } from "./vault-model"

export type Scope = "account" | "machine" | "mesh" | "browser"
export type SettingsSection = { id: string; label: string; scope: Scope; icon: LucideIcon; keywords: string; about?: string; wide?: boolean }

// Grouped by where a setting lives, so every pane says who it affects (Apple HIG; VS Code machine scope).
export const sections: SettingsSection[] = [
  { id: "account", label: "Account", scope: "account", icon: Users, keywords: "email sign out everywhere tailscale user login" },
  { id: "general", label: "General", scope: "machine", icon: Server, keywords: "version platform hostname status stop gateway agents restart" },
  { id: "sharing", label: "Sharing", scope: "machine", icon: Globe, keywords: "public access https mcp address tailscale allow list funnel proxy" },
  { id: "diagnostics", label: "Diagnostics", scope: "machine", icon: Activity, keywords: "issues errors traces crash logs resolve" },
  { id: "replication", label: "Replication", scope: "mesh", icon: RefreshCw, keywords: "mesh peers machines enroll sync remove schema",
    about: "Enrolled machines share accounts, chat history, limits and Library data, and every member is fully trusted. Enroll on both machines to start syncing." },
  { id: "connections", label: "Vault", scope: "mesh", icon: KeyRound, keywords: "mcp oauth login sign in credentials token header variable linear notion vault secrets agents cursor claude codex grok",
    about: "Sign in once. Every enrolled machine can use it." },
  { id: "agents", label: "Agents", scope: "mesh", icon: Sparkles, keywords: "mcp agentpack tools subagents task helpers defaults new chats permissions bypass auto approve" },
  // Role tables need more than the form width.
  { id: "omp", label: "Oh My Pi", scope: "mesh", icon: Bot, keywords: "omp models roles thinking agent", wide: true,
    about: "One configuration for every Oh My Pi session. Changes apply to new chats. A setting you never change keeps each machine’s own value." },
  { id: "notifications", label: "Notifications", scope: "browser", icon: Bell, keywords: "push alerts badge needs you turn finishes limit offline" },
  { id: "appearance", label: "Appearance", scope: "browser", icon: Palette, keywords: "theme dark light system accessibility screen reader terminal voiceover" },
]

export const groups: { scope: Exclude<Scope, "account">; title: string }[] = [
  { scope: "machine", title: "This machine" },
  { scope: "mesh", title: "All machines" },
  { scope: "browser", title: "This browser" },
]

export function matches(item: { label: string; keywords: string }, query: string) {
  const words = query.toLowerCase().split(/\s+/).filter(Boolean)
  const text = `${item.label} ${item.keywords}`.toLowerCase()
  return words.every((word) => text.includes(word))
}

// Counts that need a look, shown on the row like System Settings' badges.
export function useBadges(): Record<string, { text: string; attention: boolean } | undefined> {
  const live = useEventsConnected()
  const issues = useQuery({ ...issuesQuery("open"), refetchInterval: live ? false : 30_000 }).data
  const connections = useQuery(connectionsQuery(live)).data
  const replication = useQuery({ ...replicationQuery, refetchInterval: live ? false : 30_000 }).data
  const signIn = vaultBadge(useQuery(sharedSignInsQuery).data ?? [], connections?.credentials ?? [])
  const stuck = (replication?.peers ?? []).filter((peer) => {
    const contact = replication?.contacts?.[peer.node]
    return contact?.error || contact?.needs_update || contact?.needs_trust || contact?.reachable === false
  }).length
  return {
    diagnostics: issues?.length ? { text: String(issues.length), attention: issues.some((issue) => issue.regressed) } : undefined,
    connections: signIn ? { text: `${signIn} to sign in`, attention: true } : undefined,
    replication: stuck ? { text: `${stuck} not syncing`, attention: true } : undefined,
  }
}
