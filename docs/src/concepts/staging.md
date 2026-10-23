# Staging and Bundles

Staging materializes cached packages into the directory layout each harness expects. Every harness has its own conventions, so `sync` builds a separate staging tree per harness and per [mode](./modes.md).

## Staging stays outside your repo

Staged trees are deliberately kept out of your project's Git repository. They are machine-specific, regenerated on demand from the cache, and can be large. agentpack also does **not** symlink pack content into your real `~/.claude` or `~/.cursor` — that would leak project-specific pins into your global agent config.

Only `agentpack.toml` and `pack.lock` belong in version control. (Two opt-in exceptions exist: the `agentpack agent` and `agentpack agy` launchers create a single workspace symlink — see their harness guides.)

## Staging root

The default staging root is a per-project directory in the system temp location:

```text
<temp_dir>/agentpack-<project-hash>/
```

Set `AGENTPACK_STAGING_ROOT` to pin a stable location — useful when your OS rotates temp directories, or in CI for a fast local disk:

```sh
export AGENTPACK_STAGING_ROOT=/tmp/agentpack-staging
```

Each rebuild creates a private generation under `modes/<mode>-g-<id>/`. A small `modes/<mode>.current` file selects the verified generation for new commands. Each harness gets a directory inside it:

```text
<staging-root>/
  modes/
    default.current              # selected generation's directory name
    default-g-<id>/
      plugins/agentpack-bundle/   # Claude --plugin-dir bundle
      opencode/                   # OpenCode config root (OPENCODE_CONFIG_DIR)
      codex-home/                 # legacy Codex staging path
      cursor/  cursor-home/       # Cursor plugin tree + fake HOME
      grok/                       # Grok bundle; home is durable (see Grok guide)
      agy/                        # Antigravity workspace plugin
```

A launcher points its harness at the right staged tree by setting an environment variable or CLI flag before running the binary — `claude --plugin-dir …`, `OPENCODE_CONFIG_DIR`, `CODEX_HOME`, `GROK_HOME`, a fake `HOME` for Cursor, a workspace `--add-dir` for Antigravity. Codex uses a separate published home generation per project and mode so running daemon sessions keep their original tree. The [harness guides](../harnesses/claude.md) cover each one.

Mutable state is kept outside disposable staging. Codex session directories, prompt history, and SQLite indexes resolve to `~/.codex`, while project-scoped MCP OAuth credentials resolve under `$AGENTPACK_HOME/projects/<hash>/codex-mcp-oauth/`; Grok sessions resolve to `~/.grok/sessions`; Cursor state resolves through its real data directory. Claude, OpenCode, and Antigravity already keep session data in their native user locations because agentpack does not redirect those data roots.

## What a bundle contains

A **bundle** is what one package contributes to a staging tree. Packages carry some mix of:

- **commands** — slash-command definitions
- **agents** — sub-agent or mode definitions
- **skills** — reusable instruction sets
- **rules** — persistent context applied across a session
- **MCP servers** — merged into each harness's native MCP config

[Cross-harness conversion](../harnesses/conversion.md) happens here: a skill authored in one format is re-rendered into every target harness's native format rather than copied verbatim. Layering order within a tree is user config first, then plugins (by `cache_key`), then bare skills, then the project's `./.agents/` overlay — later layers win on the same relative path.

## Re-staging

`sync` rebuilds every harness's staging from the current cache state:

```sh
agentpack sync
```

The replacement is published only after verification. An active launch keeps its original paths, so another launch, `sync`, or `lock` can update the environment without replacing that session's files. Failed rebuilds leave the published generation selected. Previous staging generations are retained, including MCP support files; they use disk space until removed outside active sessions. The per-generation `.staged.json` file records the content checked by `sync --verify-only`.

Commands that take longer than one second report elapsed progress on stderr. `lock` also names each package as it resolves it. Use `--no-progress` to suppress those lines, or `--quiet` to keep only warnings and errors. Progress stops before handing control to a harness or opening the mode editor.

Launchers run a **fast pre-sync** when `agentpack.toml`, `pack.lock`, and `./.agents/` are unchanged since the last successful launch: they compare input and cache metadata and skip the full lock-resolve, re-download, and rebuild. The fast path does not re-hash staged files. Set `AGENTPACK_FULL_VERIFY=1` to hash cached package content on every launch; use `sync --verify-only` to check staged files. Because of that, floating pins do not advance on launch alone — run `agentpack sync` or `agentpack lock --update` when you need the lock refreshed.
