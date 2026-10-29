# OpenCode

`agentpack opencode` launches [OpenCode](https://github.com/sst/opencode) with your native settings and the selected pack.

## What the launcher does

Agentpack stages converted pack content. Both OpenCode 1 and 2 keep their native config root and receive pack content through launch settings; OpenCode 1 also uses a generated plugin.

Extra arguments are forwarded:

```sh
agentpack opencode --model anthropic/claude-sonnet-4-6
agentpack --yolo opencode      # permits actions for this launch only
```

## What the root contains

```text
opencode/
  opencode.json          # generated settings for OpenCode 1
  agentpack-config.json  # generated settings for OpenCode 2 or a custom OpenCode 1 root
  agents/   commands/   skills/    # converted pack artifacts
  agentpack-hooks/       # bridge and generated support files
```

Global, project and custom settings, plugins and assets are read from their original locations. Agentpack inspects native MCP and LSP names to avoid replacing them with pack servers. User settings and CLI preferences remain in their native locations.

Native compatibility fixtures have been run on Linux with OpenCode 1.17.11, 1.19.5 and 2.2.2. The isolated skill probe still validates only the 1.17 series and uses a staged config root with `--pure`; it does not observe the normal launch's plugin bridge or external plugins.

## Artifact handling

| Artifact | Staged as |
|---|---|
| Commands | OpenCode command markdown |
| Agents | OpenCode agent markdown |
| Skills | OpenCode skill |
| Rules | Best-effort skill fallback |
| MCP | Merged into generated OpenCode settings |

## Attribution

OpenCode has no first-class attribution setting. agentpack writes an `agentpack-no-attribution.md` system-prompt file and adds it to `instructions[]` in the generated OpenCode settings — prompt-level guidance, the best available lever. Set `AGENTPACK_KEEP_ATTRIBUTION=1` to skip it.

## Inspecting the staged root

```sh
agentpack sync
ls "$AGENTPACK_STAGING_ROOT/modes/default/opencode/"   # if AGENTPACK_STAGING_ROOT is set
```

## Environment

| Variable | Effect |
|---|---|
| `OPENCODE_PATH` | Path to the `opencode` binary |
| `AGENTPACK_STAGING_ROOT` | Override the staging root |

See [Environment Variables](../reference/env-vars.md) for the complete list.
