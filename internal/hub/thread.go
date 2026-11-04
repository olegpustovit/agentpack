package hub

import "github.com/olegpustovit/agentpack/internal/acp"

// ReduceThread folds conversation log entries for the hub TUI (same shape as the web reducer).
func ReduceThread(entries []acp.LogEntry) acp.ThreadState {
	return acp.ReduceLog(entries)
}
