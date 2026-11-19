package acp

import (
	"encoding/json"
	"math"
)

// Usage is what one log entry adds to a session's counters. Input excludes cache reads and writes for every
// agent, so the four token counts add up to everything the turn processed.
type Usage struct {
	Turns, InputTokens, OutputTokens, CacheReadTokens, CacheWriteTokens int64
	// CostMicros is cost this entry adds; CostTotalMicros is a session's running total. -1 when not reported.
	CostMicros, CostTotalMicros int64
	Model                       string
}

type tokenCounts struct {
	Input       int64 `json:"inputTokens"`
	Output      int64 `json:"outputTokens"`
	Thought     int64 `json:"thoughtTokens"`
	CachedRead  int64 `json:"cachedReadTokens"`
	CachedWrite int64 `json:"cachedWriteTokens"`
	// Grok's names; its input includes the cache reads.
	CacheCreation int64 `json:"cacheCreationTokens"`
	CostUSDTicks  int64 `json:"costUsdTicks"`
}

// ParseUsage normalizes the usage an agent reports in one log entry; ok is false when it reports none.
// One shape per agent: ACP turn_end.usage (Claude, Codex, OMP, Antigravity), Grok's turn_end._meta,
// and usage_update.cost, a running total (Claude, OMP). Cursor reports nothing, so its turns only count.
func ParseUsage(entry LogEntry) (Usage, bool) {
	var payload struct {
		Subagent string       `json:"_subagent"`
		Usage    *tokenCounts `json:"usage"`
		Cost     *struct {
			Amount   float64 `json:"amount"`
			Currency string  `json:"currency"`
		} `json:"cost"`
		Meta struct {
			Model   string       `json:"_claude/model"`
			ModelID string       `json:"modelId"`
			Grok    *tokenCounts `json:"usage"`
			Quota   struct {
				Models []struct {
					Model  string `json:"model"`
					Counts struct {
						Total int64 `json:"totalTokens"`
					} `json:"token_count"`
				} `json:"model_usage"`
			} `json:"quota"`
		} `json:"_meta"`
	}
	usage := Usage{CostMicros: -1, CostTotalMicros: -1}
	// A subagent's own running totals and turns belong to its child session, not this one.
	if json.Unmarshal(entry.Payload, &payload) != nil || payload.Subagent != "" {
		return usage, false
	}
	switch entry.Kind {
	case "turn_end":
		usage.Turns = 1
		if counts := payload.Usage; counts != nil {
			// Reasoning is billed as output.
			usage.InputTokens, usage.OutputTokens = counts.Input, counts.Output+counts.Thought
			usage.CacheReadTokens, usage.CacheWriteTokens = counts.CachedRead, counts.CachedWrite
		} else if counts := payload.Meta.Grok; counts != nil {
			usage.InputTokens, usage.OutputTokens = max(0, counts.Input-counts.CachedRead), counts.Output
			usage.CacheReadTokens, usage.CacheWriteTokens = counts.CachedRead, counts.CacheCreation
			// xAI bills in ticks of 1e-10 USD.
			usage.CostMicros = counts.CostUSDTicks / 10_000
		}
		usage.Model = payload.Meta.ModelID
		// Claude lists helper models (such as a title model) too; the one that did most of the work names the turn.
		var most int64 = -1
		for _, model := range payload.Meta.Quota.Models {
			if model.Counts.Total > most {
				usage.Model, most = model.Model, model.Counts.Total
			}
		}
		return usage, true
	case "usage_update":
		usage.Model = payload.Meta.Model
		if cost := payload.Cost; cost != nil && (cost.Currency == "" || cost.Currency == "USD") {
			usage.CostTotalMicros = int64(math.Round(cost.Amount * 1e6))
		}
		return usage, usage.CostTotalMicros >= 0 || usage.Model != ""
	}
	return usage, false
}
