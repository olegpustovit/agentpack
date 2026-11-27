// Settings sections without their panes, so the System sidebar can list them without loading any.
import { useQuery } from "@tanstack/react-query"
import { Activity, Bell, Bot, Sparkles, Globe, KeyRound, Settings, type LucideIcon } from "lucide-react"
import { useEventsConnected } from "@/features/events/use-events"
import { connectionsQuery } from "./connections-model"
import { issuesQuery } from "./diagnostics-model"
import { sharedSignInsQuery, vaultBadge } from "./vault-model"

// Top-level sections are the sidebar; the rest open from General, which they go back to.
export type SettingsSection = { id: string; label: string; icon: LucideIcon; parent?: "general"; wide?: boolean }

export const sections: SettingsSection[] = [
  { id: "general", label: "General", icon: Settings },
  { id: "connections", label: "Vault", icon: KeyRound },
  { id: "notifications", label: "Notifications", icon: Bell },
  { id: "sharing", label: "Sharing", icon: Globe, parent: "general" },
  { id: "agents", label: "Agents", icon: Sparkles, parent: "general" },
  // Role tables need more than the form width.
  { id: "omp", label: "Oh My Pi", icon: Bot, parent: "general", wide: true },
  { id: "diagnostics", label: "Diagnostics", icon: Activity, parent: "general" },
]

// Counts that need a look: Vault on its sidebar row, Diagnostics on its General row.
export function useBadges(): Record<string, string | undefined> {
  const live = useEventsConnected()
  const issues = useQuery({ ...issuesQuery("open"), refetchInterval: live ? false : 30_000 }).data
  const connections = useQuery(connectionsQuery(live)).data
  const signIn = vaultBadge(useQuery(sharedSignInsQuery).data ?? [], connections?.credentials ?? [])
  return {
    diagnostics: issues?.length ? `${issues.length} open` : undefined,
    connections: signIn ? `${signIn} to sign in` : undefined,
  }
}
