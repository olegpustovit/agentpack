# ACP conformance (WP0)

Optional local gate — **not CI**. Run:

```bash
./scripts/acp-conformance/run
```

That checks shared `testdata/acp/full.jsonl` reducers (Go `internal/acp`, `internal/hub`, Vitest `reducer.test.ts`). With `AGENTPACK_ACP_PROBE_PROJECT` set to a synced project, it also sends one real prompt to each agent through the worker's ACP path (`TestLiveACPProbe`) and writes the log to `$TMPDIR/acp-<harness>.jsonl`. Committed transcripts under `testdata/acp/<harness>.jsonl` keep only the staged test skill in command lists; personal user-scope commands are removed.

The probe does not stage: run the harness's launcher once in the project first (for example `CURSOR_AGENT_PATH=/bin/true agentpack agent`), because staging generations are shared and another harness's rebuild leaves Cursor's fake home and the Antigravity overlay to be refreshed by their own launch. `AGENTPACK_ACP_PROBE_IMAGE=<png>` sends an image the way the gateway stores an upload.

Image prompt (a solid red 32×32 PNG, "What single colour fills this image?"): claude, codex, cursor, agy, omp and grok all answered "Red".

Live run, 2026-11-04, Linux ARM64, a project with one staged skill (`gws-drive-upload`), prompt "Reply with exactly the word: ok":

| Harness | Result | Staged skill seen |
|---|---|---|
| claude | ok | yes (after fixing adapter exec bit and plugin shape) |
| codex | ok | yes |
| cursor | ok | yes |
| agy | ok | yes, once `agentpack agy` or a chat start has created the workspace overlay |
| omp | ok | yes |
| opencode | ok after `session/set_config_option` model `anthropic/claude-fable-5` | its default ACP model `opencode/crouton-alpha-free` needs an OpenCode sign-in and refuses with "Authentication required"; the chat notice now says to pick a signed-in model |
| grok | ok (after the owner signed in) | yes |


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
