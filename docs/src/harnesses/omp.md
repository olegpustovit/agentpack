# Oh My Pi (OMP)

`agentpack omp` launches [Oh My Pi](https://github.com/can1357/oh-my-pi)
with the selected pack through OMP's additive `--plugin-dir` flag.

```sh
agentpack omp
agentpack omp --profile work --model openai/gpt-5.2
agentpack --yolo omp
agentpack omp -- --mode rpc  # protect OMP flags that overlap agentpack flags
```

Install OMP separately. Set `OMP_PATH` to select an executable outside PATH.
Agentpack forwards OMP arguments and preserves HOME, `PI_CONFIG_DIR`,
`PI_CODING_AGENT_DIR`, and profile selection. OMP keeps managing native settings,
provider credentials, OAuth accounts, plugins, sessions and resume history.
Agentpack does not copy or replace those stores.

## Pack content

The selected staging generation contains an `omp/` plugin with skills, commands,
agents, native rules, MCP and LSP configuration. Mode filtering, package integrity
checks, support files and staging verification use the shared agentpack pipeline.
OMP also discovers native project and user content itself, including `.agents`
skills; agentpack does not duplicate those project skills into its plugin.
Native sources retain OMP's precedence and collision behavior. Agent model selectors,
thinking level and OMP task fields are retained. Tool denials become explicit
allowlists; deny-only lists grant the validated OMP built-ins except the denied
tools, excluding custom tools. Scoped Claude tool permissions cannot be represented
and grant no built-in tools, with a warning. `spawns` is omitted when task is not
allowed, so OMP cannot re-add a denied task tool.

MCP servers use `.mcp.json`, with stdio commands, arguments, environment and
working directories, HTTP/SSE URLs, headers, enabled state and OAuth client
settings. OMP namespaces plugin MCP names with `agentpack-bundle:`. OAuth metadata
URL overrides have no matching OMP field and produce a warning. LSP servers use
OMP's plugin `.lsp.json` support; native project/user LSP configuration has higher
priority. OMP resolves the language-server executables when it starts.

An always-on rule carries attribution and pinned-plugin guidance. Set
`AGENTPACK_KEEP_ATTRIBUTION=1` to omit attribution guidance.

## Hooks

Agentpack renders canonical hooks into an OMP JavaScript extension under
`hooks/pre/`. The extension invokes agentpack's shared hook executor with the
staged package root available through the plugin-root environment variables.
Commands inherit the launch working directory. Native OMP hook modules in package
`hooks/pre/` and `hooks/post/` directories are also staged.

| Canonical event | OMP event and behavior |
|---|---|
| PreToolUse | `tool_call`: block, replace input, attach context |
| PermissionRequest | `tool_call`: runs before every tool; allow preserves native approval, ask blocks |
| PostToolUse / PostToolUseFailure | `tool_result`: success/failure filtering, output replacement, context |
| UserPromptSubmit | `input` in TUI/RPC; `before_agent_start` in print/JSON: block or append context |
| SessionStart | `session_start`: run commands and deliver context before the first agent turn |
| SessionEnd | `session_shutdown`: notification only |
| Stop | `session_stop`: blocking requests a continuation |
| PreCompact | `session.compacting`: add summary context; cannot cancel compaction |

Other canonical events produce omission diagnostics. Session notifications,
permission timing and compaction cancellation differences produce degradation
diagnostics. A tool-hook execution failure blocks that tool call. The generated bridge honors
shorter configured command timeouts and caps each event's total execution at
25 seconds, before OMP's 30-second extension deadline. Timeout blocks pre-tool and
input actions; hooks needing more time must be shortened or run asynchronously. Prompt and
agent handlers use agentpack's shared Codex-based executor, so they require a
working Codex installation and credentials.

## Usage limits

The gateway reads `omp usage --json --no-extensions` without inference calls.
Provider/account windows stay separate; unavailable percentages remain unknown.
Native OMP usage caches may be updated by this command.

## Validation

The integration targets OMP 18.14.0. Run the native discovery fixture with:

```sh
AGENTPACK_TEST_OMP=/absolute/path/to/omp go test ./internal/harness/omp
```

This fixture starts the released RPC runtime without inference and verifies
converted skills, slash commands, agents, rules and attribution discovery.
Hook bridge tests check blocking, input/output changes, context and continuation.
The compiled CLI integration fixture uses a local model stub and real MCP/LSP
protocol peers; it verifies named-profile native skills, settings preservation,
headless input denial, hook execution, server calls and session resume after a
staging rebuild. It makes no external inference calls:

```sh
AGENTPACK_TEST_OMP=/absolute/path/to/omp go test ./integration -run '^TestOMPRealRuntimeWithPackHooksMCPAndLSP$'
```

The CI `OMP native runtime` job runs both fixtures against pinned OMP and Bun releases.
