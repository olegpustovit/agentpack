# ACP conformance (WP0)

Optional local gate — **not CI**. Run:

```bash
./scripts/acp-conformance/run
```

That checks shared `testdata/acp/full.jsonl` reducers (Go `internal/acp`, `internal/hub`, Vitest `reducer.test.ts`). Manual harness probes (real agents, staged skills, images, permissions) belong here next; record transcripts under `testdata/acp/<harness>.jsonl`.

Verified harness ACP surface (2026-11-02, j7 §1):

| Harness | ACP | Images | resume | Notes |
|---|---|---|---|---|
| claude | `@agentclientprotocol/claude-agent-acp@0.100.0` | yes | yes | plugins via `_meta.claudeCode.options` |
| codex | `@agentclientprotocol/codex-acp@2.5.0` | yes | yes | `CODEX_HOME` staged |
| opencode | `opencode acp` | yes | yes | `OPENCODE_CONFIG_DIR` |
| cursor | `cursor-agent acp` | yes | no resume in probe | five `cursor/*` extensions |
| grok | `grok agent stdio` | yes | yes | needs `authenticate` when logged out |
| agy | `agy acp` | not advertised | yes | workspace plugin overlay |
| omp | `omp acp` | yes | yes | `--plugin-dir` staging |

Gaps discovered in-repo only (not re-probed each run): see artifact [j7 PLAN.md](https://artifacts.microapps.space/a/j7t4c503fsmt) WP0 for live staging checks.
