package state

import (
	"context"
	"database/sql"
)

// AgentDefaults are the mesh-wide defaults for ACP chats.
type AgentDefaults struct {
	AgentpackTools     bool `json:"agentpack_tools"`
	ReplaceSubagents   bool `json:"replace_subagents"`
	BypassPermissions  bool `json:"bypass_permissions"`
	AutoApproveHelpers bool `json:"auto_approve_helpers"`
}

func ReadAgentDefaults(ctx context.Context, db interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (AgentDefaults, error) {
	var result AgentDefaults
	err := db.QueryRowContext(ctx, `SELECT
		coalesce(max(CASE WHEN key='agentpack_tools' THEN value END), 1),
		coalesce(max(CASE WHEN key='replace_subagents' THEN value END), 1),
		coalesce(max(CASE WHEN key='bypass_permissions' THEN value END), 0),
		coalesce(max(CASE WHEN key='auto_approve_helpers' THEN value END), 0)
		FROM agent_defaults`).
		Scan(&result.AgentpackTools, &result.ReplaceSubagents, &result.BypassPermissions, &result.AutoApproveHelpers)
	return result, err
}
