# DevFest demo

A short walkthrough of a project using one pinned skill across coding agents.
Install Agentpack and at least one supported agent, and sign in to that agent before the talk. Use a disposable project and review the skill before demonstrating it.

```sh
mkdir agentpack-demo && cd agentpack-demo
agentpack init
agentpack add anthropics/skills/skills/canvas-design
agentpack sync
agentpack sync --verify-only
```

Show `agentpack.toml` and `pack.lock`: the manifest declares the dependency and the lock records its resolved commit and content hash. Launch an installed agent with the matching command, for example `agentpack claude` or `agentpack opencode`, and show its native skill discovery if available. A staged skill is not proof that an agent follows it.

For the browser view, run `agentpack gateway start` and, on first use, `agentpack gateway setup`. Open the printed URL and complete setup with its one-time token. Show the Library and an actual local session. Avoid displaying credentials, private paths or other people's sessions.

If the network or an agent account is unavailable, show the [local skill transcript](output.txt), the [control-plane capture](control-plane.png), and the [evaluation notes](../src/guides/icse-evaluation.md). The recorded ICSE results use their own pinned versions; they are not evidence for every current agent release.
