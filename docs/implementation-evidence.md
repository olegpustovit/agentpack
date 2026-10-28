# v0.9.1 implementation evidence

Starting commit: `7004652234cbd73e0d7f656a024c947a79af2d67` (v0.8.7). The main implementation is `6ace12b5ed83edb461eb0f168c10d452272aa55b` (v0.9.0); the release endpoint is the commit identified by tag `v0.9.1`, including Windows CI corrections. Existing terminal-close cleanup changes were preserved. This delivery covers [PLAN.md](../PLAN.md) and the [gateway reference](gateway-workspace-plan.md).

## Required invariants

| ID | Implementation and evidence |
| --- | --- |
| R01 | Separate definition/workspace roots and external bindings. `TestCompiledCLIExternalEnvDoesNotWriteCheckout`, relocation fixtures, and the isolated newcomer demo check content, modes and links rather than relying on Git status. |
| R02 | Frozen restoration retains exact lock bytes and refuses missing locked inputs. `TestRestoreFrozenLeavesLockBytesUnchanged` and `TestRestoreFrozenFailsMissingInputWithoutLockChange`. Semantic `sha256-source-v1` identity remains separate from the lock-byte hash. |
| R03 | Preflight reads without layout initialization, cache normalization, repairs, native execution, or network calls. `TestPreflightDoesNotNormalizeCachedMarketplace` guards the previously mutating cache check. The Linux fixture runner traces process/network syscalls and snapshots all owned fixture trees. |
| R04 | Shared package, dot-agents, hook, MCP, LSP and harness configuration projections feed staging and reporting. Captured inputs are revalidated before publication. Actual generated-output tests cover all six MCP adapters, native seed winners, plugin path substitution, Codex overlays, Claude settings, inherited config shapes with preserved array order, LSP output and generation rotation. Native-discovered configuration outside these owned outputs retains explicit unknown coverage. |
| R05 | Property-level declared/generated/observed/unknown evidence, typed reports, and semantic contract validation. `TestContractUsesNamedEffectiveArtifactsAndPropertyEvidence`. |
| R06 | Native absence requires complete relevant inspection plus observed completeness. Partial catalogs cannot satisfy required absence; missing/stale evidence remains unknown. Receipt and contract fixtures cover malformed and conflicting records. |
| R07 | Strict external mode refuses workspace overlays before materialization; invalid explicit selectors never fall through. `TestPreflightPlannedWritesMatchStrictSyncRefusal` and external-context CLI tests. |
| R08 | Human and JSON output share normalized findings, consequences and remediation. Typed input failures return 2, violations 3, required unknown/stale evidence 4, operational failures 5. Published schema/sample compatibility and JSON/human agreement tests cover the formats. |
| R09 | Existing native auth/history continuity is preserved. `TestRestoredExternalDefinitionsKeepNativeStateAndIsolateModes` exercises two definitions, two modes, and a failed probe using synthetic credentials/history. Secrets stay out of bundles, capsules and registry objects. |
| R10 | Verified staging generations publish through mode pointers; live commands keep old paths. `TestRestoreDuringLiveLaunchKeepsOriginalGeneration`, `TestStagingGenerationPublishesOnlyOnSuccessAndPinsReaders`, and failed/interrupted rebuild tests cover publication and rollback. |
| R11 | Validated portable export/import and separately redacted support exports. Tests reject traversal, symlinks, oversized entries, future schemas and digest mismatches without replacing working state. Local path dependencies are explicitly refused by portable export. |
| R12 | Existing project workflows, v2/v3 lock readers and argument forwarding remain covered by the full repository suite. New source identities are versioned; older receipts need a fresh probe. No native behavior across untested versions is implied. |

## Gateway delivery

The browser implements all five required phases: the reference window/tokens and Sessions interactions; installs with per-destination state and restart choice; persisted Library/Machines/history/restore flows; opt-in registry replication and reviewable offline drafts; and PWA/reconnect timing.

Registry objects are content-addressed and checked before publication. Credential values are removed before hashing, with private local bindings kept outside replicated objects. Name updates replicate independently from revision heads. Tombstones wait for enrolled-peer receipts before collection; concurrent revisions retain two heads. Restore backs up both definition files, uses the normal frozen staging path, and recovers interrupted mutations. Package records do not transfer arbitrary project trees or local-only cache bytes; drafts name missing holders and never substitute a pin or auto-apply.

Network tests cover enrollment, identity spoofing rejection, digest mismatch, durable acknowledgment, deletion propagation, bounded peer routes and draft review. Registry tests cover redaction, parentage, naming convergence, two heads, rollback and recovery. Browser tests use mocked HTTP/WebSockets; they test interactions and payloads, not a live multi-machine Tailscale deployment.

Browser evidence: 29 desktop Chromium checks, one phone Chromium check, and 29 desktop WebKit checks passed. After the final text/CSS polish, Chromium and phone passed again and both browsers passed the complete review-capture flow. Captures cover 1440, 1024 and 390 widths in light/dark, plus Library, Machines, install, restore, drafts and replication settings. Chromium's persistent-profile `Page.getInstallabilityErrors` returned an empty list against the actual Go gateway with the latest embedded assets.

The optional host-color query and server-side screen snapshot were not added. The interface labels fallback colors, tracks the actual replay watermark, retains a dim last frame during reconnect, and disables input until control ownership is revalidated.

## Commands, formats and samples

Implemented command syntax is documented in [Portable environments](src/concepts/environments.md):

```sh
agentpack env init team --from /path/to/definition
agentpack env use team --project /path/to/checkout
agentpack --env team --workspace /path/to/checkout env restore
agentpack --env team --workspace /path/to/checkout preflight --agent claude --strict-external --json
agentpack --env team --workspace /path/to/checkout probe --agent claude
agentpack --env team --workspace /path/to/checkout preflight --agent claude --receipt latest --json
agentpack config compare RECEIPT_A RECEIPT_B
agentpack support export RECEIPT_ID --output ./support-capsule.json
agentpack env export team.bundle --from team
agentpack env import team.bundle --name imported-team
```

`env bind` aliases `env use`; `--definition-root` also selects an external directory. Existing harness commands launch it using `--env`. `--` preserves native argument forwarding. `env restore` performs frozen restoration; it has no `--agent` flag.

[Schema documentation](../schemas/README.md) maps the shipped field names to the plan. [Clean](../evaluation/icse/reports/clean.json), [violation](../evaluation/icse/reports/violation.json), [unknown](../evaluation/icse/reports/unknown.json), [stale receipt](../evaluation/icse/reports/stale-receipt.json), and [sanitized support](../evaluation/icse/reports/support.json) samples are generated by the real CLI. The stale/support examples deliberately use a synthetic receipt with no native observations. They are not fabricated native evidence.

## Native scope and observed effects

Controlled native sentinel tests passed on Linux arm64 with Claude Code 2.1.312 and Codex 0.159.3. [Run metadata](../evaluation/icse/native-evidence-linux-arm64.json) records executable hashes, commands and limits. Earlier recorded macOS arm64 evidence covers Claude 2.1.296 and Codex 0.159.2. Parser-compatible version ranges are broader than those tested binaries.

Both adapters observed a benign restored skill. Removing it through an independently authored empty mode removed its positive observation; the required observed contract stayed unknown under partial coverage. Source IDs agreed, frozen lock bytes were unchanged, and fixture checkouts stayed unchanged. These observations establish positive discovery in a controlled projection, not complete ambient absence, model use or arbitrary source attribution.

Native probes requested no model inference or production MCP/hook execution. Native network and global runtime effects were not measured. Diagnostic writes are isolated and private; normal authentication/history still follows each native harness's supported continuity path. Production OAuth refresh races and Windows descendant-process cleanup were not validated here.

## Reproduction and final gates

Local release gates passed: `make fmt-check`, `go vet ./...`, `go test ./...`, `go test -race ./...`, `go mod verify`, `goreleaser check`, the release build, and all 12 traced preflight cases. The final LSP diagnostic change also passed the staging/sync race suites. Published JSON examples passed schema validation. The independent design review reached its quality bar and scored both final documentation findings resolved; its final disposition was `ship`.

```sh
make fmt-check
go vet ./...
go test ./...
go test -race ./...
go build -o /tmp/agentpack-check ./cmd/agentpack
python3 evaluation/icse/run.py --binary /tmp/agentpack-check --trace --output evaluation/icse/reports
AGENTPACK_NATIVE_PROBES=1 go test -v ./internal/harness/claude ./internal/harness/codex -run '^TestNative.*Sentinel$'
AGENTPACK_NATIVE_PROBES=1 go test -v ./internal/cli -run '^TestNativeRestoredPackConformance$'
```

From `internal/network/web`, run `npm ci && npm run build`, then `npm test`. `npm run check:pwa -- http://127.0.0.1:7420/` checks a running Go gateway with a persistent browser profile. Set `PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH` when using a system Chromium installation.

The [two external examples](../evaluation/icse/environments/README.md) provide a short offline walkthrough. An independent agent followed those documented commands in an isolated home: minimal preflight succeeded with optional staging warnings; review lock/restore/preflight succeeded; quiet mode violated the generated-skill requirement with exit 3; default mode recovered. The checkout remained empty. The strongest fault beyond a package lock is an undeclared inherited configuration/skill: the fixture runner catches both a user settings file and a project-native skill while the lock hash stays unchanged, then verifies recovery after removing those authored inputs.

Release validation uses the existing GoReleaser workflow, including Windows credentials/launch checks, package tests, generated frontend verification, installer tests, archives and checksums. Live cross-machine replication and Windows power-loss durability were not exercised locally. Registry transfer batches currently have a 64 MiB limit; previous staging generations are retained rather than automatically collected.

The broader Windows CI run for v0.9.0 caught platform-specific path and fixture assumptions after the narrower release checks had passed. v0.9.1 corrects those cases, and release publication now depends on the full Windows Go suite. The published samples above retain their measured v0.9.0 build identity.
