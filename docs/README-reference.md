# Installation and reference

See the [entry README](../README.md) for a local example and current limits.

## Install

Prebuilt binaries are published for every release. Pick whichever fits your platform.

### Installer script

macOS and Linux:

```bash
curl --proto '=https' --tlsv1.2 -LsSf https://github.com/olegpustovit/agentpack/releases/latest/download/agentpack-installer.sh | sh
```

Windows PowerShell:

```powershell
irm https://github.com/olegpustovit/agentpack/releases/latest/download/agentpack-installer.ps1 | iex
```

Both installers write only the executable to `$HOME/.local/bin` by default. They do not edit shell profiles or the user `PATH`; use `AGENTPACK_INSTALL_DIR` to select another userspace directory.

### Homebrew

```bash
brew install --cask olegpustovit/tap/agentpack
```

Upgrade with `brew upgrade --cask agentpack`. Served from the shared tap
[olegpustovit/homebrew-tap](https://github.com/olegpustovit/homebrew-tap) (`brew tap olegpustovit/tap && brew install --cask agentpack` also works).

### Prebuilt binaries

Download an archive for your platform from the [latest release](https://github.com/olegpustovit/agentpack/releases/latest), extract it, and put `agentpack` on your `PATH`. Supported targets:

| Platform | Target |
| --- | --- |
| macOS (Apple Silicon) | `darwin_arm64` |
| macOS (Intel) | `darwin_amd64` |
| Linux (x86-64) | `linux_amd64` |
| Linux (ARM64) | `linux_arm64` |
| Windows (x86-64) | `windows_amd64` |

Each release includes a SHA-256 `checksums.txt` covering every archive.

### From source

From a clone of this repository, with Go 1.26 or newer:

```bash
go install ./cmd/agentpack
# or
make install   # release build to ~/.local/bin (override: make install INSTALL_DIR=/usr/local/bin)
```

**Prerequisites (source builds only):** Go 1.26 or newer.

## Experimental Claude proxy mode

`agentpack --proxy claude` starts Claude Code through an experimental, supervised
Anthropic-compatible proxy backed by OpenAI/Codex credentials. The proxy is started for that Claude
session, `ANTHROPIC_BASE_URL` and `ANTHROPIC_AUTH_TOKEN` are injected into the child process, and the
proxy is shut down when Claude exits.

```bash
agentpack --proxy claude
```

Authentication is resolved from `OPENAI_API_KEY` first, then from Codex auth (`~/.codex/auth.json`,
the Codex keychain entry, or agentpack's shared Codex auth file). The Go proxy core follows
the Codex/OpenAI path in [raine/claude-code-proxy](https://github.com/raine/claude-code-proxy);
thanks to [@raine](https://github.com/raine) for the original project and reference behavior.

Proxy diagnostics are written as JSONL under
`$AGENTPACK_HOME/projects/<project-hash>/proxy-logs/` by default. Set
`AGENTPACK_PROXY_LOG_DIR` to override the directory. Logs omit request payloads unless
`AGENTPACK_PROXY_LOG_PAYLOADS=1` is set.

## Cross-harness artifact conversion

agentpack does not just copy files — it **parses** each artifact and **re-renders** it in the target format:

| Kind | Native form for | Converted to |
|---|---|---|
| **Skills** | all six agents | |
| **Slash commands** | Claude Code, OpenCode, Cursor, Grok | Codex: skill; Antigravity: loaded as a skill |
| **Subagents** | all six agents (Codex: custom agent file `agents/<name>.toml`) | Codex: also a skill |
| **Rules** | Cursor, Antigravity; Codex, OpenCode (`AGENTS.md` and a skill) | Claude Code, Grok: skill |
| **Hooks** | Claude Code, Codex, Cursor, Grok, Antigravity | OpenCode: generated plugin |
| **MCP servers** | all six agents, each in its own MCP configuration file | |
| **Language servers** | Claude Code, OpenCode, Grok | none (warning: no setting) |

Conversions are reported as warnings. Subagents a package keeps in subdirectories of `agents/` are staged at the top of `agents/` for agents that read only that level. See the [conversion guide](https://github.com/olegpustovit/agentpack/blob/dev/docs/src/harnesses/conversion.md) for the full matrix.

## Manifest example

```toml
name = "my-project"
version = "0.1.0"

[dependencies]
"github.com/anthropics/skills/skills/canvas-design" = { branch = "main" }
"github.com/anthropics/claude-plugins-official/plugins/hookify" = { branch = "main" }
"github.com/my-org/internal-tools/skills/deploy" = { tag = "v2.1.0" }

[modes.default]
base = "all"
disable = ["package-path:github.com/my-org/internal-tools/skills/deploy:commands/noise.md"]

[modes.design]
base = "all"
disable = ["mcp:filesystem"]
```

## Commands

| Command | Description |
|---|---|
| `init` | Create `agentpack.toml` and `pack.lock` in the current project |
| `add <spec>` | Add a dependency, resolve, lock, and sync |
| `remove <spec>` | Remove a dependency and re-sync |
| `lock` | Resolve manifest and regenerate `pack.lock` (`--update` advances floating pins) |
| `sync` | Download, cache, and rebuild staging for all harnesses |
| `mode ...` | Create, inspect, edit, and manage project-local modes |
| `mcp ...` | Add, remove, and list MCP servers |
| `extra sync-claude` | Reconcile local skills between `.claude/skills` and `.agents/skills` |
| `eject [AGENT...]` | Write staged content into the project for IDEs and agents started without agentpack (`--remove` undoes it) |
| `claude` | Sync + launch Claude Code with `--plugin-dir` |
| `agent` | Sync + launch Cursor Agent with staged `HOME` (alias: `cursor-agent`) |
| `opencode` | Sync + launch OpenCode with `OPENCODE_CONFIG_DIR` |
| `codex` | Sync + launch Codex with `CODEX_HOME` |
| `grok` | Sync + launch Grok with `GROK_HOME` |
| `agy` | Sync + launch Antigravity with the project as a workspace dir |
| `completion <shell>` | Generate Bash, Zsh, Fish, or PowerShell completions |

Redirected harness homes do not make resume history temporary: Codex history is shared with `~/.codex`, Grok sessions with `~/.grok/sessions`, and legacy temp-staging history is imported on upgrade without overwriting native files.

## Releasing (maintainers)

Releases are automated by [GoReleaser](https://goreleaser.com): pushing a `v*` tag triggers
`.github/workflows/release.yml`, which builds per-platform binaries (macOS arm64/x86_64, Linux
x86_64/arm64, Windows x86_64), creates archives and checksums, publishes the GitHub Release, and
pushes the generated Homebrew cask to [olegpustovit/homebrew-tap](https://github.com/olegpustovit/homebrew-tap).

| Goal | Command |
|---|---|
| Bump minor + tag + push (→ CI release) | `make ship-minor` |
| Bump patch + tag + push (→ CI release) | `make ship-patch` |
| Bump version only (no release) | `make minor` / `make patch` |

Requires a repo secret **`HOMEBREW_TAP_TOKEN`** (a PAT with write access to `olegpustovit/homebrew-tap`) so
CI can push the cask across repos.

