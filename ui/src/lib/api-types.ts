// Hand-written from the Go JSON structs. Keep field names in step with the json tags.

export type Status = { setup_required: boolean; authenticated: boolean }

export type Identity = {
  node: { id: string; name: string; os: string; version: string }
  auth: string
  tailscale_user: string
}

export type TailnetNode = { id: string; name: string; online: boolean }

export type Actor = { kind: string; user?: string; node?: string }

export type TerminalState = {
  interactive: boolean
  running: boolean
  restarting: boolean
  shared_input?: boolean
  responder?: number
  rows: number
  columns: number
  exit_code: number
}

export type SessionInfo = {
  id: string
  name?: string
  directory: string
  definition_root?: string
  folder_id?: string
  harness: string
  mode: string
  state: TerminalState
  started_at?: string
  started_by?: Actor
  last_used?: string
}

export type TopologyPeer = {
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
  self: { id: string; name: string; os: string; relay: string }
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
  launcher: string
  status: string
  package: string
  version: string
  integrity: string
  host: string
  reason: string
}

export type ProjectRow = {
  directory: string
  name: string
  folder_id: string
  available: boolean
  unavailable_reason: string
  last_used: string
  launches: ProjectLaunch[]
  packages: ProjectPackage[]
  mcp: ProjectMCP[]
  error: string
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
  sharing_enabled: boolean
  allowed_peers?: string[]
}

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

export type PeerReplica = { node: string; receipt_count: number; digests: string[] }

export type ReplicationPeers = {
  peers: PeerReplica[]
  contacts: Record<string, { at: string; error?: string }>
}
