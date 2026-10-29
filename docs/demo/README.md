# Local skill demo

The [transcript](output.txt) and [image](local-skill.png) come from the actual CLI, version 0.7.6, built from source revision 40c3a0cb0cf68342fd11ceec825799fecc64efa7. Documentation-only changes accompany this capture. Environment: Go 1.26.6, Linux ARM64, Debian bookworm-slim container with no network and no host home or credentials mounted.

The [script](run.sh) initializes a project, adds a local skill without syncing, performs a full sync and runs staged-file verification. It checks the recorded hash and asserts that six staged skill files contain the fixture instruction, one per adapter. The full paths are retained in the disposable output directory's staged-paths.txt.

## Reproduce

Build the binary from the repository root. Docker is used only to isolate the demo from your real agent settings:

```sh
go build -o /tmp/agentpack-demo-cli ./cmd/agentpack
agentpack_demo_dir=$(mktemp -d)
cp docs/demo/run.sh "$agentpack_demo_dir/run.sh"
docker run --rm --network none --read-only \
  --tmpfs /tmp --tmpfs /root \
  -v /tmp/agentpack-demo-cli:/agentpack:ro \
  -v "$agentpack_demo_dir:/demo" \
  debian:bookworm-slim /bin/sh /demo/run.sh
```

The image must be present locally before the network-disabled run. The binary is built for the host architecture. The demo creates only disposable project/cache/staging files in the mounted directory and the container's temporary filesystem. It does not install or launch an agent, execute an MCP server, call a model, resolve a GitHub package or test Windows/macOS behavior.

Five existing compiled-CLI integration tests also passed: help/version, initialization and overwrite refusal, local add, staging for every harness, and changed-staging detection/replacement. Those checks are separate from native agent discovery or instruction following. The complete test suite, race checks, installers and release publication were not run for this documentation change.

## Control plane screenshot

`control-plane.png` is the full-resolution capture and `control-plane-poster.png` is a second capture framed for the README. Both show the real local gateway's Library after adding the
public `anthropics/skills/skills/canvas-design` package to a disposable project.
It was captured from revision `ac958b17e5c651a1980979c2b82c4d938e8e4ec7` with
the current harness changes in the working tree
with an isolated `AGENTPACK_HOME`, tailnet sharing disabled, Chromium in light
mode and device scale factor 2. The full capture uses a 1440 × 900 viewport;
the poster uses 1100 × 550 so the same real interface remains legible at README
width. The gateway data and
account were created for the capture. The Library imports the cache entry; it
does not display the project's lockfile hash in this view.

The disposable project used `agentpack init`, `agentpack add anthropics/skills/skills/canvas-design`
and `agentpack sync`. A locally built binary served the isolated data with
`agentpack gateway serve --listen 127.0.0.1:18479`. After signing in to the disposable account, Playwright opened
Library, selected the package and captured the page with
`await page.screenshot({ path: "capture.png" })` after the layout settled.
