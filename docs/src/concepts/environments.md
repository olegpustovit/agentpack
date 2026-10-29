# Portable Environments

Agentpack can keep `agentpack.toml` and `pack.lock` **outside** a git checkout so a teammate or CI job can restore the same locked inputs without committing harness-specific setup into every repository.

## Three roots

| Object | Role |
|---|---|
| **WorkspaceRoot** | Agent cwd / checkout. Source of `.agents/` inputs and any compatibility-mode overlays. |
| **DefinitionRoot** | Directory holding `agentpack.toml` and `pack.lock` (and definition-relative path deps). |
| **Binding** | Machine-local map from a workspace path to a definition root, stored under `$AGENTPACK_HOME/projects/<hash>/environment.toml`. |

By default, the workspace is the directory where you run `agentpack`. The definition is discovered by searching that directory and its parents for `agentpack.toml` or `pack.lock`. For example, running `agentpack claude` in `/work/project1` uses `/work/agentpack.toml` when present and launches Claude in `/work/project1`. Gateway sessions, recent folders, and fresh launches use that same project directory.

`--workspace` overrides the working directory. Legacy `--project-root` alone sets both roots to the supplied path. Use `--definition-root` or `agentpack env use` to select a definition outside the workspace's parent directories.

## Portable source identity

Reports and receipts identify the selected definition with `sha256-source-v1:<digest>`. This digest covers a canonical semantic envelope: the parsed manifest, the default `contract.json` when present, normalized locked package inputs and hashes, disabled-plugin selection, and pinned MCP package trees. TOML comments and formatting do not affect it; ordered arrays such as MCP arguments and contract requirements do. The separate `lock_digest` remains the SHA-256 of the exact `pack.lock` bytes for byte-level audit and receipt freshness.

The source identity omits cache keys, local aliases, lock serialization details, timestamps, and host paths. A local path dependency contributes through its locked package content hash, not its machine path. MCP environment/header values and OAuth client secrets are omitted; their names and non-secret server structure remain. Recognizable credential-flag values in MCP arguments and credential URL components are replaced with placeholders. Command argument values that do not use recognizable credential flags remain part of the identity, so avoid embedding secrets in arbitrary argument strings. Custom `--contract` files affect the selected policy digest, not the immutable source identity; the definition's default `contract.json` is included.

## Commands

```sh
# Create an external definition (optionally copied from an existing project)
agentpack env init team --from /path/to/project
agentpack env use team --project /path/to/checkout   # alias: env bind

# Explicit selector (fails if missing; never falls through to another env)
agentpack --env team --workspace /path/to/checkout preflight --agent claude

# Share validated manifest, unchanged lock, and optional contract
agentpack env export team.bundle --from team
agentpack env import team.bundle --name team

# Fetch locked cache trees and rebuild staging without amending pack.lock
agentpack env restore

# Pure offline check (no lock/cache/staging/repo writes)
agentpack preflight --agent claude --json --policy local
agentpack --strict-external preflight --agent opencode --policy ci --contract ./contract.json
```

Exit codes for `preflight` / `probe`: `0` ready (including warnings), `2` usage/policy error, `3` violated, `4` required observation unknown, `5` operational failure, `1` unexpected.

## Native probe

```sh
agentpack sync
agentpack probe --agent claude          # plugin list + skill inventory via --plugin-dir
agentpack probe --agent codex           # validated positive static-projection discovery
agentpack --strict-external probe --agent claude --policy ci
agentpack --strict-external preflight --agent claude --policy ci --receipt latest
```

Probes are explicit (never inside pure preflight). Receipts land under `$AGENTPACK_HOME/projects/<hash>/probe-receipts/`. Claude plugin/skill presence and Codex positive skill discovery can be observed in isolated `controlled_projection` scope. Require that scope explicitly in a contract. Recorded native conformance runs cover Claude Code 2.1.296 and Codex 0.159.2 on macOS arm64, and Claude Code 2.1.312 and Codex 0.159.3 on Linux arm64. The adapters also record `workspace_session`: the catalog of a session started in the checkout, with an observed `winner` per packaged skill (see [contracts](../reference/contracts.md)). The parsers accept later patch versions in these series; that compatibility range is broader than the versions actually tested. User-level configuration, source digests, model use and complete ambient absence remain unknown. `--policy` and `--strict-external` on a probe bind receipt freshness to the later preflight policy, and `--receipt latest` selects the latest successful current-format record.

```sh
agentpack support export RECEIPT_ID --output ./support-capsule.json
```

Support capsules retain diagnostic codes, evidence grades, coverage, status, and public binary versions. They omit free-form native messages, remedies, arbitrary property values, evidence paths, and private artifact names because these can contain credentials. Review before sharing; nothing uploads automatically.

Supported **strict external** launch targets (no checkout configuration writes): **Claude** (`--plugin-dir` / `--settings`) and **OpenCode** (native configuration plus launch settings). Cursor and Antigravity still require workspace overlays in compatibility mode; `--strict-external` refuses them before writing.

## Preflight vs `sync --verify-only`

`sync --verify-only` can still resolve dependencies, restore missing cache entries, upsert index rows, drop colliding staged skills, and rewrite the staged manifest. **`preflight` does none of that.** Missing cache in offline preflight is a finding with a restore remedy, not permission to fetch.

## Inheritance policy

- **`local` (default)** — report inherited user/global configuration as info/warnings.
- **`ci`** — same findings, with MCP pin gaps and (under `--strict-external`) undeclared inheritance treated as violations.

A green report means the enumerated configuration checks passed for the selected target and policy. It does not claim the agent is secure, or that LLM answers are reproducible.

## Safe sharing

Exports contain `agentpack.toml`, the unchanged `pack.lock`, optional `contract.json`, and a versioned per-file SHA-256 manifest. Bindings, caches, receipts, raw logs, auth, and history are excluded. Export refuses symlinked definition files, inline MCP environment values, recognizable credential arguments or URL credentials, and host-private absolute paths; supply secrets through the launching environment. This conservative check cannot infer every possible secret embedded in an arbitrary string. Review the definition before sharing.

Host-local path dependencies are refused, including relative paths: the current bundle does not include their file trees. Publish these resources as pinned GitHub packages before exporting. A bundle is a portable definition, not an offline cache archive; restore still needs the locked remote inputs or a verified cache.

Import accepts only the documented regular files, rejects duplicates, traversal, symlinks, files over 4 MiB, unsupported schemas, invalid definitions/contracts/locks, and digest mismatches. It validates in a private temporary directory and publishes only when complete. The destination must not already exist, and export also refuses an existing output file. Older bundles without `bundle.json` must be re-exported. SHA-256 protects against corruption, not an untrusted publisher: inspect a received definition before launching it.

## Rebuild recovery and active launches

Rebuilds serialize publishers, prepare and verify a new `modes/<mode>-g-<id>/` generation, then publish it through `modes/<mode>.current`. Running commands retain their selected generation's paths, so a restore can publish new configuration while the old session continues. Failed preparation leaves the previous published generation usable. Previous generations are retained for live support files; this is not automatic disk reclamation. The staging-root override is partitioned by definition identity, and Grok homes are partitioned by mode and generation.

Codex additionally publishes private home generations under the project's durable state. Configuration generations remain separate from shared credential/history sources. Returned failures and interrupted staging transactions recover only managed roots; recovery rejects unexpected paths and symlinked backup directories. Auth/history links are moved as links and are never followed during rollback. Compatibility-mode workspace overlays remain adapter-managed; strict external mode refuses targets requiring them.

Claude's allowlist and attribution settings are stored per project/mode and passed as inline `--settings` JSON. They cannot overwrite another environment's allowlist, and `CLAUDE_CONFIG_DIR` stays unchanged so the native credential namespace remains available.

Continuity tests cover two external definitions, two modes, a restore during a live launch, and a failed probe. They check generated-setting isolation and preservation of synthetic Codex auth/history. They do not exercise production OAuth refresh races or establish behavior for untested native versions.

`env init --from` validates a complete copy before publication and includes an optional contract. Relative local dependencies are rebased to their original absolute source paths so relocation cannot silently select a different local package. These copies remain machine-local and cannot be exported until path dependencies are replaced by pinned remote packages. New destinations publish atomically; existing empty user directories retain their identity, with no-overwrite file publication and rollback on returned failures. Initialization refuses nonempty destinations.
