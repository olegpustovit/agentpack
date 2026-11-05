package network

import (
	"context"
	"os"
	"sync"
	"time"
)

// AgentSession is one resumable agent-side conversation from session/list.
type AgentSession struct {
	ID    string `json:"id"`
	Title string `json:"title,omitempty"`
	Cwd   string `json:"cwd,omitempty"`
}

type agentSessionCacheEntry struct {
	at      time.Time
	entries []AgentSession
	err     error
}

var agentSessionCache sync.Map

const agentSessionCacheTTL = 30 * time.Second

// ListAgentSessions probes the harness ACP adapter for prior sessions in a workspace.
func ListAgentSessions(ctx context.Context, home, directory, harness, definitionRoot, modeName string) ([]AgentSession, error) {
	_ = home
	key := directory + "\x00" + harness + "\x00" + modeName
	if cached, ok := agentSessionCache.Load(key); ok {
		entry := cached.(agentSessionCacheEntry)
		if time.Since(entry.at) < agentSessionCacheTTL {
			return entry.entries, entry.err
		}
	}
	if os.Getenv("AGENTPACK_TEST_ACP") == "3" {
		entries := []AgentSession{{ID: "prior-1", Title: "Test", Cwd: directory}}
		agentSessionCache.Store(key, agentSessionCacheEntry{at: time.Now(), entries: entries})
		return entries, nil
	}
	entries, err := probeHarnessAgentSessions(ctx, WorkerLaunch{
		Transport: "acp",
		Directory: directory,
		Session: SessionInfo{
			Directory:      directory,
			DefinitionRoot: definitionRoot,
			Harness:        harness,
			Mode:           modeName,
		},
	})
	if err == nil {
		agentSessionCache.Store(key, agentSessionCacheEntry{at: time.Now(), entries: entries})
	}
	return entries, err
}
