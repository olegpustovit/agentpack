# OMP integration research

Upstream: https://github.com/can1357/oh-my-pi
Source inspected: `6a86df8539fccf3ad7818081266e554022fc21bc`.

OMP accepts repeatable `--plugin-dir` arguments. This is an additive source;
agentpack can preserve native profiles, settings, credentials and sessions
without redirecting HOME, PI_CONFIG_DIR, or PI_CODING_AGENT_DIR.

Relevant upstream sources:

- `docs/config-usage.md`: settings layers, profiles, native and foreign discovery.
- `src/cli/flag-tables.ts`: `--plugin-dir` and `--config` parsing.
- `src/discovery/helpers.ts`: injected local plugin roots.
- `src/discovery/claude-plugins.ts`: skills, commands, rules, hooks, MCP.
- `src/task/discovery.ts`: subagent discovery, including local plugin roots.
- `src/config/mcp-schema.json`: stdio cwd, enabled, HTTP/SSE and OAuth fields.

Plugin MCP names are namespaced by the plugin name. Native OMP sources have
higher discovery priority than plugin sources. Plugin manifest lookup prefers
`.omp-plugin/plugin.json` and falls back to `.claude-plugin/plugin.json`.
MCP uses manifest `mcpServers` or conventional `.mcp.json`.

OMP supports native rule files with `alwaysApply` and globs. Hooks are executable
JS/TS extension modules, not Claude shell-hook JSON. Supporting agentpack's
canonical hook events requires an OMP extension renderer and runtime validation.

## Verified behavior

The released npm OMP 18.14.0 runtime was exercised with Bun 1.4.4. The
`TestNativeDiscovery` fixture starts its RPC host and checks converted skills,
commands, agents, rules and attribution. No inference is requested by that fixture.

`TestOMPRealRuntimeWithPackHooksMCPAndLSP` builds and runs the real agentpack CLI
with a local OpenAI-compatible model stub. It verifies native profile skill
loading alongside pack skills, always-on rules, session and prompt hook context,
pre-tool blocking, input replacement, post-tool output replacement, an MCP call,
and language-server initialization. The model calls mounted MCP/LSP tools through
OMP's default `write xd://...` interface. Native profile settings and model files
remain unchanged. A persistent native session resumes after a staging rebuild.
A denied print-mode prompt exits nonzero without reaching the model endpoint.

The test exposed OMP's print/JSON hosts skipping `input` events; the generated
bridge runs those hooks from `before_agent_start` and aborts denied submissions.
OMP's explicit final exit otherwise overwrote the refusal status, so the bridge
preserves that status through native cleanup and exit.

The full Go suite and targeted native fixtures pass. The CI `OMP native runtime`
job pins both runtimes and repeats the native fixtures without account credentials
or external inference. Release configuration validation checks the pinned installer
commands and release assets; the release workflow validates supported packages.

OMP-only manifests and marketplace catalogs are accepted. Existing accepted
package normalization stays unchanged: OMP catalogs are a fallback when no legacy
catalog is present, and no OMP manifest is synthesized into existing packages.
The pinned normalized-tree digest test continues to pass.

## Native boundaries

OMP keeps ownership of native profiles, settings, provider credentials, OAuth and
session storage. The launcher only supplies its staged plugin and optional yolo
flag. Source fingerprints cover native OMP configuration and resources while
excluding credential stores and runtime databases. Native credential acquisition
and remote-provider OAuth availability remain OMP/provider responsibilities.

The normal preflight reports generated staging evidence; OMP does not add a
native-observation adapter to `agentpack probe`. Like the other harnesses without
that adapter, it makes no unsupported claims about observed winners. Native
runtime verification lives in the explicit integration fixtures above.

See `docs/src/harnesses/omp.md` for hook degradation, tool-policy conversion and
unsupported OAuth metadata URL overrides.
