// Hand-written from the Go JSON structs. Keep field names in step with the json tags.

export type Status = {
  joining?: boolean
  join_phase?: "waiting_for_approval" | "waiting_for_accounts" | "ready"
  node_name?: string
  claimed: boolean
  legacy: boolean
  authenticated: boolean
  email: string
}

export type Identity = {
  node: { id: string; name: string; os: string; version: string; schema_version?: number; main?: boolean }
  auth: string
  tailscale_user: string
}

export type TailnetNode = { id: string; name: string; online: boolean; main?: boolean; needs_update?: boolean; version?: string; schema_version?: number }

// agent: the harness of the chat that acted for the user through agentpack tools.
export type Actor = { kind: string; user?: string; node?: string; agent?: string }

export type TerminalState = {
  shell_command?: "idle" | "running"
  interactive: boolean
  running: boolean
  restarting: boolean
  shared_input?: boolean
  responder?: number
  rows: number
  columns: number
  exit_code: number
}

export type AgentCapabilities = {
  loadSession?: boolean
  promptCapabilities?: { image?: boolean }
  sessionCapabilities?: {
    resume?: Record<string, unknown>
    fork?: Record<string, unknown>
  }
  // A steer joins the running turn instead of interrupting it.
  steering?: boolean
}

export type SessionInfo = {
  provisionalStart?: "claim" | "replica"
  node_id?: string
  reachable?: boolean
  last_sync?: string
  ended_at?: string
  archived_at?: string
  // Closed: out of the sidebar, still in History. Reopening clears it.
  closed_at?: string
  id: string
  name?: string
  // First user prompt of an ACP chat, one line. Older peers omit it.
  title?: string
  directory: string
  definition_root?: string
  // Main folder of the agentpack worktree the session runs in. Older peers omit it.
  worktree_of?: string
  folder_id?: string
  harness: string
  mode: string
  state: TerminalState
  transport?: string
  agent_session_id?: string
  agent_capabilities?: AgentCapabilities
  conversation_state?: string
  // The gateway will resume this stopped chat once sign-ins have synced; older peers omit it.
  resume_pending?: boolean
  started_at?: string
  started_by?: Actor
  last_used?: string
  // Last conversation activity; older peers and terminal sessions omit it.
  last_event_at?: string
  // Older peers omit both.
  goal?: GoalState
  subagents?: SubagentSummary[]
  // Background work an idle chat will wake for (internal/acp WaitItem); older peers omit it.
  waiting?: WaitItem[]
  // The chat whose agent started this helper, and whether this chat's agent may use agentpack tools.
  parent_id?: string
  tools?: boolean
}

export type GoalLimits = { minutes?: number; cost_usd?: number; tokens?: number; turns?: number }

// Mirrors the worker's goal record (also the payload of goal_update log entries).
export type GoalState = {
  objective: string
  checks?: string
  state: "active" | "paused" | "blocked" | "limited" | "met" | "cleared"
  engine?: "native" | "gateway"
  started_at?: number
  active_ms?: number
  resumed_at?: number
  turns?: number
  last_reason?: string
  limits?: GoalLimits
  updated_at?: number
}

// One piece of open background work. `until` (unix ms) bounds it; `peer` is the chat an ask waits on.
export type WaitItem = { kind: "shell" | "monitor" | "agent" | "task" | "wake" | "reply"; until?: number; peer?: string }

// Running subagents of a chat, for the sidebar's child rows.
// state "idle" is a finished subagent (the sidebar hides it); older peers list only busy ones.
export type SubagentSummary = { id: string; title?: string; state: string; started_at?: number; ended_at?: number }

export type AgentSession = {
  id: string
  title?: string
  cwd?: string
}

export type TopologyPeer = {
  main?: boolean
  id: string
  name: string
  os: string
  state: string
  connection: string
  relay: string
  rtt_ms?: number
  last_seen?: string
  version?: string
  // Always 0 from the server; the UI fills it from the sessions query.
  sessions: number
}

export type Topology = {
  self: { id: string; name: string; os: string; relay: string; main?: boolean }
  peers: TopologyPeer[]
}

export type ProjectLaunch = { harness: string; mode: string; last_used: string }

export type ProjectPackage = {
  module: string
  commit: string
  content_hash: string
  cache_key: string
  kind: string
  name: string
  url: string
  direct: boolean
}

export type ProjectMCP = {
  name: string
  source: string
  module?: string
  disabled?: boolean
  launcher: string
  status: string
  package: string
  version: string
  integrity: string
  host: string
  reason: string
  /** Set for a remote server a shared MCP login can serve; absent from older peers. */
  url?: string
  /** The login this project picked for url; absent means the first (default) login. */
  login?: string
  project_login?: boolean
  transport?: string
}

// What one mode turns off; a peer that predates modes sends none.
export type ProjectMode = {
  name: string
  off_packages: string[]
  partial_packages: string[]
  off_mcp: string[]
}

export type ProjectRow = {
  directory: string
  name: string
  folder_id: string
  available: boolean
  unavailable_reason: string
  details_deferred?: boolean
  last_used: string
  launches: ProjectLaunch[]
  packages: ProjectPackage[]
  mcp: ProjectMCP[]
  modes: ProjectMode[]
  error: string
  // The checkout's state; absent when the folder is not a Git repository or the peer predates it.
  git?: { branch?: string; head: string; dirty: number }
}

export type LimitWindow = {
  id: string
  label: string
  used_percent?: number
  remaining_percent?: number
  duration_seconds?: number
  reset_at?: string
  reset_text?: string
}

export type LimitSnapshot = {
  node_id?: string
  reachable?: boolean
  last_sync?: string
  harness: string
  state: string
  source?: string
  observed_at: string
  plan?: string
  error?: string
  windows: LimitWindow[]
}

export type LimitsResponse = { limits: LimitSnapshot[] }

export type Settings = {
  running: boolean
  platform: string
  can_manage?: boolean
  sharing_enabled: boolean
  allowed_peers?: string[]
  public?: PublicAccess
}

// Public access: an HTTPS origin served beside the loopback one. `candidate` is a detected proxy or Funnel.
// `catch_all`: the proxy forwards any host name, so nothing is suggested and the owner types the address.
export type PublicAccess = { enabled: boolean; origin?: string; pinned?: boolean; candidate?: string; catch_all?: boolean }

export type Package = {
  id: string
  schema?: number
  module: string
  direct?: boolean
  commit: string
  content_hash?: string
  kind: string
  url: string
  owner: string
  repo: string
  path?: string
  cache_key: string
  name?: string
  source_node?: string
}

export type Agentpack = { id: string; name: string; revisions: string[]; heads: string[] }

// internal/registry/catalog.go. Plugins carry an add `spec`, MCP servers an `agentpack mcp add` command.
export type CatalogItem = {
  kind: "plugin" | "mcp"
  name: string
  description?: string
  publisher?: string
  source: string
  spec?: string
  command?: string
  server?: MCPServerInput
  url?: string
}
// What POST /api/mcp/servers installs: a command with args, or a URL with a type.
export type MCPServerInput = { name: string; command?: string; args?: string[]; url?: string; type?: string }
export type CatalogSource = { name: string; count: number; error?: string }
export type Catalog = { items: CatalogItem[]; sources: CatalogSource[] }

export type Revision = {
  id: string
  schema?: number
  agentpack_id: string
  parent_ids?: string[]
  manifest: string
  lock: string
  package_ids?: string[]
  source_node?: string
  project?: string
  created_at: string
  label?: string
  mode?: string
  needs_local_configuration?: boolean
}

export type AgentpackWithRevisions = { agentpack: Agentpack; revisions: Revision[] }

export type LatestVersion = { commit: string; committed_at?: string; current: string[]; exact: boolean }

export type AgentpackConflictField = { path: string; name: string; base: string; values: Record<string, string> }
export type AgentpackConflict = { heads: string[]; fields: AgentpackConflictField[] }

export type PackagePin = { commit: string; content_hash?: string; cache_key: string }

export type Draft = {
  id: string
  item: string
  revision?: string
  node: string
  directory: string
  reason: string
  pack_id?: string
  folder_identity: string
  expected_pins: Record<string, PackagePin>
  needed_holder?: string
  created_at: string
}

export type PeerReplica = { node: string; mesh?: boolean }

export type ReplicationPeers = {
  peers: PeerReplica[]
  contacts: Record<string, { at: string; last_sync?: string; reachable?: boolean; error?: string; needs_update?: boolean; needs_trust?: boolean; schema_version?: number; version?: number; lag?: number }>
}
