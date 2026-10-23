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
