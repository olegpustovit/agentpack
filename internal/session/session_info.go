package session

import (
	"time"

	"github.com/olegpustovit/agentpack/internal/acp"
)

type AgentCapabilities struct {
	LoadSession         bool                      `json:"loadSession,omitempty"`
	PromptCapabilities  *AgentPromptCapabilities  `json:"promptCapabilities,omitempty"`
	SessionCapabilities *AgentSessionCapabilities `json:"sessionCapabilities,omitempty"`
	// Steering: a steer joins the running turn instead of interrupting it (harness.ACPLaunch.Steers).
	Steering bool `json:"steering,omitempty"`
}

type AgentPromptCapabilities struct {
	Image bool `json:"image,omitempty"`
}

type AgentSessionCapabilities struct {
	Resume *struct{} `json:"resume,omitempty"`
	Fork   *struct{} `json:"fork,omitempty"`
}

type SessionInfo struct {
	NodeID            string             `json:"node_id,omitempty"`
	Reachable         bool               `json:"reachable"`
	LastSync          time.Time          `json:"last_sync,omitzero"`
	EndedAt           time.Time          `json:"ended_at,omitzero"`
	ArchivedAt        time.Time          `json:"archived_at,omitzero"`
	LastUsed          time.Time          `json:"last_used,omitzero"`
	LastEventAt       time.Time          `json:"last_event_at,omitzero"`
	FolderID          string             `json:"folder_id,omitempty"`
	ID                string             `json:"id"`
	Name              string             `json:"name,omitempty"`
	Title             string             `json:"title,omitempty"`
	Directory         string             `json:"directory"`
	DefinitionRoot    string             `json:"definition_root,omitempty"`
	Harness           string             `json:"harness"`
	Mode              string             `json:"mode"`
	Transport         string             `json:"transport,omitempty"`
	AgentSessionID    string             `json:"agent_session_id,omitempty"`
	AgentCapabilities *AgentCapabilities `json:"agent_capabilities,omitempty"`
	ConversationState string             `json:"conversation_state,omitempty"`
	State             TerminalState      `json:"state"`
	StartedAt         time.Time          `json:"started_at,omitzero"`
	StartedBy         *Actor             `json:"started_by,omitempty"`
	Goal              *Goal              `json:"goal,omitempty"`
	Subagents         []SubagentSummary  `json:"subagents,omitempty"`
	// Waiting is the background work an idle chat will wake for; older peers omit it.
	Waiting []acp.WaitItem `json:"waiting,omitempty"`
	// ParentID is the chat that owns this shell or whose agent started this helper session.
	ParentID string `json:"parent_id,omitempty"`
	// Tools lets the chat's agent call the agentpack MCP server.
	Tools bool `json:"tools,omitempty"`
	// KeepNativeSubagents is snapshotted at creation. Older sessions default to replacement.
	KeepNativeSubagents bool `json:"keep_native_subagents,omitempty"`

	// WorktreeOf is the main folder of the agentpack worktree Directory is in. Older peers omit it.
	WorktreeOf string `json:"worktree_of,omitempty"`
	// ResumePending marks a cut-off chat that resumes once sign-ins have synced. The owning gateway
	// sets it on reads; it is not stored. Older peers omit it.
	ResumePending bool `json:"resume_pending,omitempty"`
}

// TerminalState contains no command arguments, environment, or credentials.
type TerminalState struct {
	Interactive  bool   `json:"interactive"`
	Running      bool   `json:"running"`
	Restarting   bool   `json:"restarting"`
	ShellCommand string `json:"shell_command,omitempty"`
	SharedInput  bool   `json:"shared_input,omitempty"`
	Responder    uint64 `json:"responder,omitempty"`
	Rows         uint16 `json:"rows"`
	Columns      uint16 `json:"columns"`
	ExitCode     int    `json:"exit_code"`
}

// Goal is a standing objective the agent works toward across turns. Every change is logged as
// goal_update and saved on the session row, so every viewer and peer sees the same goal.
type Goal struct {
	Objective  string      `json:"objective"`
	Checks     string      `json:"checks,omitempty"`
	State      string      `json:"state"`  // active, paused, blocked, limited, met or cleared
	Engine     string      `json:"engine"` // native (the agent's /goal command) or gateway (this worker's loop)
	StartedAt  int64       `json:"started_at"`
	ActiveMS   int64       `json:"active_ms"`            // active time before the current active stretch
	ResumedAt  int64       `json:"resumed_at,omitempty"` // set only while active
	Turns      int         `json:"turns"`
	LastReason string      `json:"last_reason,omitempty"`
	Limits     *GoalLimits `json:"limits,omitempty"`
	UpdatedAt  int64       `json:"updated_at"`
}

type GoalLimits struct {
	Minutes float64 `json:"minutes,omitempty"`
	CostUSD float64 `json:"cost_usd,omitempty"`
	Tokens  int64   `json:"tokens,omitempty"`
	Turns   int     `json:"turns,omitempty"`
}

// SubagentSummary is a subagent shown under its chat in the session sidebar: every one that is
// still doing something, and the most recent that finished.
type SubagentSummary struct {
	ID        string `json:"id"`
	Title     string `json:"title,omitempty"`
	State     string `json:"state"` // running, requires_action, unknown or idle (finished)
	StartedAt int64  `json:"started_at"`
	EndedAt   int64  `json:"ended_at,omitempty"`
}
