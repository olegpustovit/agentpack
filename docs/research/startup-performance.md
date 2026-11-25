# Home-workspace startup performance

## Agent defaults launch review (2026-11-25)

A new ACP chat reads its defaults once at creation. The worker uses the
subagent setting from the session it already has on launch and resume.

Medians of five runs on Linux amd64 (Intel Core i9-12900K), before
(`f543e8a7`) and after the change.

| Benchmark | Before (ms) | After (ms) |
| --- | ---: | ---: |
| `BenchmarkMaterializationHome` | 0.766 | 0.765 |
| `BenchmarkCodexSetup/cold` | 8.574 | 8.675 |
| `BenchmarkCodexSetup/warm` | 0.134 | 0.134 |

```sh
go test ./internal/sync -run '^$' -bench '^(BenchmarkMaterializationHome|BenchmarkCodexSetup)$' -benchtime=100ms -count=5
```

## Gateway launch review (2026-11-24)

The gateway now checks invalid worktree selections before accepting a launch and
uses a client-provided session ID to identify a terminal launch after a disconnect.
On `oracle-vm` (Linux ARM64), the medians of five alternating baseline (`9802dca4`)
and updated runs met the existing setup budgets with the 32 MiB history fixture.

| Benchmark | Before (ms) | After (ms) | Budget (ms) |
| --- | ---: | ---: | ---: |
| `BenchmarkMaterializationHome` | 0.841 | 0.866 | <1 |
| `BenchmarkCodexSetup/cold` | 18.393 | 19.195 | <20 |
| `BenchmarkCodexSetup/warm` | 0.381 | 0.380 | <1 |

Build the `internal/sync` test binary for Linux ARM64 with the pinned static
library. Run it natively with `-test.run='^$'`
`-test.bench='^Benchmark(MaterializationHome|CodexSetup)$' -test.benchtime=10x -test.cpu=2`.

## Core repair WP-D (2026-11-23)

The ARM64 budget benchmarks (`BenchmarkMaterializationHome`, `BenchmarkCodexSetup/cold`
and `/warm`) keep the 32 MiB history fixture and no packages. Separate
variants add four cached, content-hashed plugins of 2,000 files
each: `BenchmarkMaterializationHomePackages` and `BenchmarkCodexSetup/warm-packages`.

```sh
go test ./internal/sync -run '^$' -bench 'Benchmark(MaterializationHome|MaterializationHomePackages|CodexSetup)$' -count 5
```

After merging core/integrate, Linux amd64 (i9-12900K), medians of five runs on a
host running other workers (load average 22 to 24 on 20 threads):

| Benchmark | Median (ms) | Range (ms) | Budget (ms) |
| --- | ---: | ---: | ---: |
| MaterializationHome | 0.721 | 0.338 to 1.258 | <1 |
| CodexSetup/warm | 0.135 | 0.113 to 0.156 | <1 |
| CodexSetup/cold | 25.434 | 12.422 to 27.778 | <20 |
| MaterializationHomePackages | 0.820 | 0.318 to 0.844 | none |
| CodexSetup/warm-packages | 21.244 | 10.496 to 22.771 | none |

Cold runs swung by 2 times with the host load, so the cold median does not
verify the budget; it still needs a run on the ARM64 reference host. With the
package set, the warm launch's remaining cost is `cache.StatFingerprint`, the
documented metadata walk of every cached file (8,000 here). WP-D removed the
capability catalog's walk of every cached file (selectors now `Lstat` one path)
and the unread `DependencyTrees`; on the same host, the fastest package-set
warm run went from 11.6 ms to 6.7 ms before the merge.

## Warm launch with cached packages (2026-11-24)

Linux x86_64 (Intel i9-12900K, Go 1.27.3), five runs with the same 32 MiB
history fixture. `warm-packages` verifies the warm path with four cached
plugins totaling 8,000 files. The proposed package-set budget is for this
x86_64 host. ARM64 package-set measurements are pending.

| Benchmark | Median (ms) | Range (ms) | Proposed budget (ms) |
| --- | ---: | ---: | ---: |
| `BenchmarkCodexSetup/warm` | 0.141 | 0.132 to 0.176 | |
| `BenchmarkCodexSetup/warm-packages` | 6.432 | 6.346 to 7.001 | <15 |

```sh
go test ./internal/sync -run '^$' -bench '^BenchmarkCodexSetup$/^(warm|warm-packages)$' -count=5
```

## Harness staging descriptions (2026-11-24)

Linux amd64, Intel i9-12900K, Go 1.27.3, with the existing 32 MiB history
fixture. Each value is the median of five runs, alternating baseline
`7bec31b` and the refactor on the same two logical CPUs.

| Benchmark | Disk before (ms) | Disk after (ms) | tmpfs fixture before (ms) | tmpfs fixture after (ms) |
| --- | ---: | ---: | ---: | ---: |
| MaterializationHome | 0.464 | 0.458 | 0.670 | 0.769 |
| CodexSetup/warm | 0.185 | 0.173 | 0.271 | 0.251 |
| CodexSetup/cold | 43.349 | 57.381 | 5.227 | 4.681 |
| CodexSetup/warm-packages | 7.484 | 7.815 | 8.464 | 7.913 |

Other jobs were active on the host. The cold disk measurements ranged from
32 to 94 ms before and 18 to 131 ms after. One refactor run with the tmpfs
fixture averaged 504 ms per operation. The shared-host measurements leave the Linux ARM64
release budget unverified.

Build each revision with `go test -c -o /tmp/agentpack-sync.test ./internal/sync`.
Alternate the binaries five times with the command below. For the tmpfs
fixture, set `TMPDIR=/dev/shm` and use `-test.benchtime=25x`.

```sh
taskset -c 2,4 /tmp/agentpack-sync.test -test.run '^$' \
  -test.bench '^(BenchmarkMaterializationHome|BenchmarkCodexSetup)$' \
  -test.benchtime=10x -test.cpu=2
```

## Private staging and lock roots (2026-11-23)

Linux amd64, Intel i9-12900K, Go 1.27.3, 32 MiB history fixture. Medians
of five runs on a shared host.

| Benchmark | Before (ms) | After (ms) |
| --- | ---: | ---: |
| MaterializationHome | 0.316 | 0.449 |
| CodexSetup/warm | 0.123 | 0.181 |
| CodexSetup/cold | 9.128 | 16.586 |

```sh
go test ./internal/sync -run '^$' -bench 'Benchmark(MaterializationHome|CodexSetup)$' -count=5
```

After the change, individual cold runs ranged from 14.8 to 46.7 ms. These
shared-host measurements leave the Linux ARM64 release budget unverified.

## State migration release check — Linux ARM64

Measured 2026-11-15 on the deployment host (`oracle-vm`, aarch64 Neoverse-N1,
two virtual CPUs), Go 1.27.3. These are native executions of the static musl
benchmark binary, using the existing 32 MiB history fixture, five repetitions
of ten iterations. They are not QEMU timings. The host was lightly loaded.

| Benchmark | Median (ms) | Budget (ms) | Result |
| --- | ---: | ---: | --- |
| MaterializationHome | 0.663780 | <1 | Pass |
| CodexSetup/cold | 17.508913 | <20 | Pass |
| CodexSetup/warm | 0.350938 | <1 | Pass |

The original 2026-10-30 reference host's hardware was not recorded, so these
numbers certify the deployment host against the same budgets without claiming
a matched-hardware before/after comparison. Reproduction: build the
`internal/sync` test binary for Linux ARM64 with the pinned static library, then
run it natively with `-test.run='^$'
-test.bench='Benchmark(MaterializationHome|CodexSetup)$' -test.benchmem
-test.benchtime=10x -test.count=5`.

## Session-store checkpoint (2026-11-14)

Five repeats of ten iterations, using the existing 32 MiB history fixtures:

| Benchmark | Linux amd64, i9-12900K | macOS arm64, Apple M1 |
| --- | ---: | ---: |
| Materialization home | 0.332408 ms | 0.799500 ms |
| Codex verified warm setup | 0.129439 ms | 0.764996 ms |
| Codex cold setup | 6.634093 ms | 42.738621 ms |

```sh
go test ./internal/sync -run '^$' -bench '^(BenchmarkMaterializationHome|BenchmarkCodexSetup)$' -benchtime=10x -count=5
```

The command took 1.53 s on Linux and 7.01 s on the physical Mac. These are medians
of the five reported values. The latest earlier shared-Linux-host measurements
were 0.342552 ms, 0.134045 ms and 6.925838 ms respectively; those used the earlier
short-run iteration count and serve as context rather than a controlled comparison.
The Linux ARM64 reference-host gate is still unverified; macOS cold staging is not
subject to that Linux budget. No full release suite or soak ran for this checkpoint.

## Original reference measurement

Measured on Linux ARM64, Go 1.26, 2026-10-30. Each result is the median of five runs with five iterations per run. These are local setup measurements, not model requests or harness process startup.

| Benchmark | Before | After |
| --- | ---: | ---: |
| Materialization fingerprint, home with 32 MiB runtime history | 26.76 ms | 0.362 ms |
| Fingerprint allocation | 17.74 MB | 0.165 MB |
| Codex rebuild, same history fixture | 33.06 ms | 11.59 ms |
| Codex verified warm setup | 0.172 ms | 0.175 ms |

The materialization and preflight fingerprints previously recursed through entire `.claude` and `.cursor` directories. Launching from home therefore read global history and rejected Cursor's `projects/.../worker.sock` as nonregular configuration. A failed rebuild could not publish its generation, so the next launch repeated setup.

Both callers now select configuration files and authored component directories explicitly. Runtime state does not invalidate materialization. Selected configuration still uses the existing strict content fingerprint, including symlink handling and nonregular-file rejection. Cache integrity checks and launch validation are unchanged.

The fixed materialization fingerprint also completed against a real home profile in a median 38.53 ms; that profile contained about 1.7 GB under `.claude`, mostly runtime history. This read-only measurement is profile-specific and is not the synthetic budget.

Reproduce the controlled comparison on the same machine and filesystem:

```sh
go test ./internal/sync -run '^$' -bench 'Benchmark(MaterializationHome|CodexSetup)$' -benchmem -benchtime=5x -count=5
```

Optional read-only profile measurement (the benchmark writes its own state only to temporary directories):

```sh
AGENTPACK_BENCH_HOME="$HOME" go test ./internal/sync -run '^$' -bench '^BenchmarkMaterializationRealHome$' -benchmem -benchtime=5x -count=5
```

The rebuild benchmark forces staging each iteration; warm iterations assert that the verified fast path is used. The socket regression uses a real Unix listener; platforms without Unix socket support skip that test. No extra dependencies or startup fast paths were added.

## Temp-directory leak (2026-11-01)

A launch in a real project spent over 4 minutes in `preparing claude…`, 51 s of it system time. A goroutine dump put it in `RemoveOrphanedProjectDirectories`, reading markers in a `/tmp` holding about 247,000 `agentpack-<hash>` directories. Each marker named a deleted `/tmp/agentpack-registry-validate-*`: `validateSnapshot` loaded its throwaway lock through the guarded loader, and a 0.12.6 gateway validated snapshots about 25 times a second.

## Switching agents and reading the Library (2026-11-02)

Measured on Windows 11 x64 with Defender on, in this repository (11 packages, one of 4,424 files), with stubbed harness binaries. These are single runs on a warm disk, not the Linux ARM64 reference benchmarks.

| Operation | Before | After |
| --- | ---: | ---: |
| Launch after another agent's launch | 3.0-4.1 s, always a rebuild | 0.19-0.25 s |
| Launch with nothing changed | 0.17-0.20 s | 0.19-0.25 s |
| Rebuild for one harness | 3.0-4.6 s | 2.4-3.3 s |
| Library bootstrap, repeated | 11-13 s | 0.44 s |
| Library package list | 0.64 s | 0.02 s |

A launch stages one harness into a new generation and publishes it, so the next agent found its own files missing and rebuilt; alternating agents never reached the fast path. Each harness now returns to its own generation while its digest holds.

One sync hashed the largest package up to four times: resolving, the verify loop, and planning and validating each tree. Planning now compares metadata, the verify loop trusts the resolve just before it, and a launch reuses a stamp from an earlier full check. The 36 s seen in the field was spent in the resolve step; it was not reproduced here, because a warm disk hashes all packages in 0.22 s. The likely cause is a first read through a cold disk and the virus scanner, which these changes skip on launches once a stamp exists, and which parallel read-ahead shortens otherwise.

The remaining rebuild cost is writing staged files and reading them back for `.staged.json`, where the profile shows opens of just-written files waiting. Hashing while writing is the next step if rebuilds matter.

`BenchmarkCodexSetup/warm` on this Windows host moved from a median 3.1 ms to 3.6 ms over 7 runs of 20 iterations, from reading the launch state to select a generation. It was not re-measured on the reference host, whose budget is 1 ms; the added work is two stats and one small file read.

Library bootstrap called `store.Get` per agentpack, and each call re-read and re-validated the whole registry, writing temporary files per revision. Objects are named by their digest, so each is now validated once per process while its metadata holds.

Validation now leaves nothing in the temp directory, and launches run the tidying in a goroutine beside the agent. Other commands wait for it at most 200 ms.

## Slow `add`, `lock` and `sync` (2026-11-04)

An `add` sat at `agentpack: add…` for over 3 minutes. Causes found:

- The shared HTTP client had no timeout, so one stalled GitHub or npm connection hung the command. It now needs response headers within 20 s and a whole request within 3 minutes.
- When codeload failed, the Git fallback cloned the whole history. It now fetches the one pinned commit at depth 1: `tanstack/router` went from 36.4 s to 5.5 s.
- A resolve downloaded the whole repository tarball once per subpath. It now reuses the last archive within one resolve.
- One command hashed each cache entry up to four times. This is now handled by the 2026-11-02 change below (planning compares metadata, launches reuse a stamp); an in-process digest memo tried here was dropped, because an explicit `sync` must always read the bytes.
- `sync` waited for temp-directory tidying; it now waits at most 200 ms.
- Packages resolved one at a time. Each wave of the dependency queue now fetches up to six in parallel and reads their dependencies in queue order, so the lock is identical. Cold `lock` of the 11 packages: 21.5 s to 13.6 s.

Warm, on the Linux ARM64 host, with this repository's 11 packages (97 MB `impeccable` entry): `sync` 2.46 s to 1.95 s, re-`add` 5.06 s to 3.06 s, `lock` 0.17 s, launch 0.07 s. Most of what remained was rebuilding staging, which copies each hook package once per harness. A plain `sync` now keeps the published staging when the last full sync's inputs digest, the agentpack binary, the rebuild journal and every staged file's content (hashed against `.staged.json`) are all unchanged; the cache is still hashed against the lock first. Unchanged `sync`: 1.95 s to 0.41 s; re-`add` of an existing dependency: 3.06 s to 1.03 s. Benchmarks after the change (medians of five): `BenchmarkMaterializationHome` 0.39 ms, `BenchmarkCodexSetup/cold` 14.9 ms, `warm` 0.15 ms.

## Idle gateway CPU (2026-11-04)

The installed gateway used 11–16% of a core while nothing happened. A CPU profile put 85% of it in library replication: every 30 s, for each peer, it decoded and validated every registry object twice, then merged heads, acknowledged and collected garbage, even when neither side had changed. `Inventory` now decodes each object once, and an exchange that finds both inventories as the last one left them returns after reading the peer's inventory. Idle CPU with five peers: 11% to 0.85%.

## MCP connection index in the launch digest (2026-11-19)

Shared MCP logins add one read of `$AGENTPACK_HOME/mcp-credentials.json` (a few hundred bytes, written by the gateway) to the launch digest, so connecting or removing a login restages the next launch. Staging reads the same file once per rebuild. Measured on an x86_64 WSL2 host (20 threads), not the ARM64 reference host, medians of five runs of 10 iterations: `BenchmarkMaterializationHome` 0.33 ms, `BenchmarkCodexSetup/cold` 6.8 ms, `warm` 0.13 ms. All are inside the budgets. The gateway's credential loop runs every 30 s with one indexed query and rewrites the file only when its bytes change.

## Per-project MCP login picks in the launch digest (2026-11-23)

A project's pick among several logins for one server lives in `$AGENTPACK_HOME/projects/<hash>/mcp-logins.json`. The launch context digests it next to the credential index, so changing a pick restages only that project's next launch; staging reads it once per rebuild. One more small file read (usually a missing-file stat) per launch. Same x86_64 WSL2 host (20 threads), not the ARM64 reference host, medians of five runs of 10 iterations:

| Benchmark | Before | After | Budget |
| --- | ---: | ---: | ---: |
| `BenchmarkMaterializationHome` | 0.39 ms | 0.39 ms | 1 ms |
| `BenchmarkCodexSetup/cold` | 7.8 ms | 8.1 ms | 20 ms |
| `BenchmarkCodexSetup/warm` | 0.14 ms | 0.16 ms | 1 ms |

```sh
go test ./internal/sync -run '^$' -bench '^Benchmark(MaterializationHome|CodexSetup)$' -benchtime=10x -count=5
```

Project permissions for plugin MCP servers add `mcp-plugin-logins.json` to the
same fingerprint. The Linux ARM64 reference host measured these medians over
five runs of 10 iterations, using `v0.14.5-dev.185` as the baseline.

| Benchmark | Before | After | Budget |
| --- | ---: | ---: | ---: |
| `BenchmarkMaterializationHome` | 0.798 ms | 0.826 ms | 1 ms |
| `BenchmarkCodexSetup/cold` | 15.105 ms | 16.695 ms | 20 ms |
| `BenchmarkCodexSetup/warm` | 0.342 ms | 0.407 ms | 1 ms |

## ACP adapter verification stamp

The adapter stores a verification stamp beside the installed npm tree after checking its full digest and confirming that its metadata stayed stable. If an entry changes or `AGENTPACK_FULL_VERIFY=1`, the next launch hashes the bytes.

On gigacomp (Linux amd64, Intel i9-12900K), five runs at 150 ms per benchmark gave these medians.

| Benchmark | Before | After |
| --- | ---: | ---: |
| Materialization home | 0.307 ms | 0.333 ms |
| Codex setup, cold | 10.139 ms | 9.719 ms |
| Codex setup, warm | 0.125 ms | 0.137 ms |

A synthetic 64 MiB installed adapter took 40.203 ms for full verification and 0.011 ms with an existing metadata stamp. The adapter benchmark measures verification alone on a one-file fixture. The launch timings above are from the amd64 development host.

```sh
go test ./internal/sync -run '^$' -bench '^Benchmark(MaterializationHome|CodexSetup)$' -count=5 -benchtime=150ms
go test ./internal/mcp -run '^$' -bench '^BenchmarkAdapterVerification$' -count=5 -benchtime=150ms
```

The same launch checks also passed on the Linux ARM64 reference host
(`oracle-vm`). Each column is the median of five runs at 150 ms per benchmark.

| Benchmark | Before (`f6e7f4b`) | After (`7bac9db`) | Budget |
| --- | ---: | ---: | ---: |
| Materialization home | 0.668 ms | 0.649 ms | 1 ms |
| Codex setup, cold | 16.263 ms | 16.255 ms | 20 ms |
| Codex setup, warm | 0.310 ms | 0.289 ms | 1 ms |

On a Linux ARM64 toolchain, run the launch command above natively. To build
the same benchmark binary on Linux amd64, use the pinned cross toolchain.

```sh
./scripts/fetch-crsqlite.sh linux arm64
CGO_ENABLED=1 CGO_LDFLAGS=-lunwind GOOS=linux GOARCH=arm64 \
  CC="$PWD/scripts/cc-crsqlite.sh linux arm64" go test -c -trimpath \
  -ldflags '-linkmode=external -extldflags=-static' -o /tmp/agentpack-launch-bench ./internal/sync
# On the reference host
/tmp/agentpack-launch-bench -test.run '^$' -test.bench '^Benchmark(MaterializationHome|CodexSetup)$' -test.count 5 -test.benchtime 150ms
```

After moving Claude's project sync into the worker, the same five-run launch
benchmarks measured 0.319 ms for materialization, 7.681 ms for cold Codex
setup, and 0.140 ms for warm Codex setup on amd64. On the ARM64 reference
host, the medians were 0.649 ms, 15.562 ms, and 0.289 ms respectively. All
three ARM64 measurements remained within budget. These figures measure
staging and generation selection. A separate smoke check confirmed that
Claude ACP 0.100.0 returns an initialize response in a temporary workspace
with no staged project configuration.

## Gateway chat shell worker

Compared the checkout with `e44312d` on Linux amd64 (Intel i9-12900K) using
the 32 MiB history fixture and five runs of ten iterations. Values are median
milliseconds per operation.

| Benchmark | Before | After |
| --- | ---: | ---: |
| Materialization home | 0.388 | 0.439 |
| Codex setup, cold | 7.763 | 7.514 |
| Codex setup, warm | 0.141 | 0.136 |

```sh
go test ./internal/sync -run '^$' -bench '^(BenchmarkMaterializationHome|BenchmarkCodexSetup)$' -benchtime=10x -count=5
```

### Launch validation without a lockfile

The benchmark command above measured medians of 0.308 → 0.336 ms for home
materialization, 7.448 → 8.455 ms for cold Codex setup, and 0.139 → 0.134 ms for
warm setup on Linux amd64 (Intel i9-12900K). These benchmarks end before
`ValidateLaunchInputs`. A separate smoke launch with `CLAUDE_CODE_PATH=/bin/true`
exited successfully from an empty folder and left both project files absent.

## One `Inputs` model and digest v2 (2026-11-23)

Launch, materialized, synced and preflight lock identities are now digests of
one `sync.Inputs`; per-harness launch inputs moved to `Harness.LaunchInputs`;
the cache metadata fingerprint adds inode and status-change time on Unix; and
package trees keep execute bits under `sha256-tree-v2`. Measured on Linux
amd64 (WSL2, 20 threads), `-count 5`, default benchtime. Before is `core/wp-n`
with only the digest v2 commit; values are median milliseconds per operation.

| Benchmark | Before | After |
| --- | ---: | ---: |
| Materialization home | 0.372 | 0.324 |
| Materialization home, packages | 0.322 | 0.328 |
| Codex setup, cold | 10.289 | 7.934 |
| Codex setup, warm | 0.138 | 0.112 |
| Codex setup, warm with packages | 6.213 | 6.052 |

The cold run is noisy on this host (one After run at 10.0 ms); no benchmark
moved beyond run-to-run spread. Not measured on the ARM64 reference host.

```sh
go test ./internal/sync -run '^$' -bench 'BenchmarkCodexSetup|BenchmarkMaterializationHome' -count 5
```

## Project Settings MCP switches (2026-11-23)

Before and after adding the project-level MCP enable switches, the Linux ARM64
reference host ran the same 32 MiB fixture benchmarks. Medians of five runs
of 10 iterations, with `v0.14.5-dev.187` as the baseline.

| Benchmark | Before | After | Budget |
| --- | ---: | ---: | ---: |
| `BenchmarkMaterializationHome` | 0.824 ms | 0.818 ms | 1 ms |
| `BenchmarkCodexSetup/cold` | 17.920 ms | 16.233 ms | 20 ms |
| `BenchmarkCodexSetup/warm` | 0.362 ms | 0.370 ms | 1 ms |

```sh
go test ./internal/sync -run '^$' -bench '^(BenchmarkMaterializationHome|BenchmarkCodexSetup)$' -benchtime=10x -count=5
```

## Atomic write consolidation (2026-11-24)

Linux amd64 (i9-12900K), five alternating runs per revision with 10 iterations
per run and the 32 MiB history fixture. The baseline was `7bec31b`.

| Benchmark | Before (ms) | After (ms) |
| --- | ---: | ---: |
| MaterializationHome | 0.536 | 0.518 |
| CodexSetup/warm | 0.201 | 0.227 |
| CodexSetup/cold | 55.137 | 57.011 |

While other builds and tests ran, ext4 cold samples ranged from 20.126 to
62.646 ms before and 20.911 to 60.265 ms after. Alternating 150 ms samples on
tmpfs yielded cold medians of 4.371 and 3.899 ms. The registry retains file and
directory syncs, and the launch path retains its existing sync behavior.

Build the binaries on the two revisions and alternate these commands five times.

```sh
go test -c -o "$bench_binary" ./internal/sync
"$bench_binary" -test.run '^$' -test.bench '^BenchmarkMaterializationHome$' -test.benchtime=10x
"$bench_binary" -test.run '^$' -test.bench '^BenchmarkCodexSetup$/^(cold|warm)$' -test.benchtime=10x
```

## Shared harness MCP projections (2026-11-24)

On Linux amd64 (Intel i9-12900K), parallel worktree builds coincided with
cold-setup ranges of 12.9–83.2 ms before and 10.3–58.0 ms after the refactor.
To reduce filesystem contention, the comparison below uses tmpfs for fixtures.
Each value is the median of five interleaved runs of 10 iterations per version,
with `7bec31b` as the baseline.

| Benchmark | Before | After |
| --- | ---: | ---: |
| `BenchmarkMaterializationHome` | 0.526 ms | 0.542 ms |
| `BenchmarkCodexSetup/cold` | 2.355 ms | 2.230 ms |
| `BenchmarkCodexSetup/warm` | 0.185 ms | 0.188 ms |

```sh
TMPDIR=/dev/shm GOMAXPROCS=4 go test ./internal/sync -run '^$' -bench '^Benchmark(MaterializationHome|CodexSetup)$' -benchtime=10x -count=5
```

## Shared generation leases (2026-11-24)

Staging and Codex share a pointer guard that protects the handoff from the
pointer read to the generation lease. Reclamation releases that guard before
checking credentials or daemons and deleting trees. Grok's durable home shares
the lease for its staged bundle.

Measured on Linux amd64 (WSL2, Intel i9-12900K) with the 32 MiB fixtures, using
`7bec31b` as the baseline. The table shows medians of five runs of ten iterations.
The baseline and changed binaries alternated using the same dedicated `TMPDIR`
on ext4. A preliminary comparison during concurrent CI measured 30.504 ms and
28.099 ms for cold setup on this host. See the
[Linux ARM64 reference check](#linux-arm64-reference-check-2026-11-24).

| Benchmark | Before | After | ARM64 budget |
| --- | ---: | ---: | ---: |
| `BenchmarkMaterializationHome` | 0.535 ms | 0.545 ms | 1 ms |
| `BenchmarkCodexSetup/cold` | 8.401 ms | 8.254 ms | 20 ms |
| `BenchmarkCodexSetup/warm` | 0.191 ms | 0.189 ms | 1 ms |
| `BenchmarkCodexSetup/warm-packages` | 7.009 ms | 7.081 ms | |

```sh
# Build the baseline and changed binaries in their respective checkouts.
go test -c -o /tmp/agentpack-sync-before ./internal/sync
go test -c -o /tmp/agentpack-sync-after ./internal/sync

# Alternate binaries for five runs each, with both using the same TMPDIR.
TMPDIR=/path/to/benchmark-tmp /tmp/agentpack-sync-before -test.run '^$' -test.bench '^(BenchmarkMaterializationHome|BenchmarkCodexSetup)$' -test.benchtime=10x
TMPDIR=/path/to/benchmark-tmp /tmp/agentpack-sync-after -test.run '^$' -test.bench '^(BenchmarkMaterializationHome|BenchmarkCodexSetup)$' -test.benchtime=10x
```

## Linux ARM64 reference check (2026-11-24)

Medians in milliseconds per operation from seven interleaved runs on
`oracle-vm`, with ten iterations per run and the 32 MiB history fixture. A
mount namespace kept both temporary roots in one private ext4 directory
under `/tmp`, so staged and durable credentials could share a hard link.

| Benchmark | Baseline (`cf98ae11`) | Before fix (`530e64ac`) | After fix | Budget |
| --- | ---: | ---: | ---: | ---: |
| `BenchmarkMaterializationHome` | 0.802 | 0.808 | 0.788 | <1 ms |
| `BenchmarkCodexSetup/cold` | 15.966 | 21.803 | 17.878 | <20 ms |
| `BenchmarkCodexSetup/warm` | 0.365 | 0.371 | 0.378 | <1 ms |

The first-parent bisect found the slowdown in merge `5303dcaf`. Within that
merge, five same-filesystem runs measured 16.684 ms at `670dc2f9` and
20.205 ms at `69131276`. The latter added fsyncs to Codex's staged OAuth
snapshots, including the rewrite during reconciliation when the existing
snapshot already matched the hard-linked credential store. Reconciliation
now keeps the matching snapshot in place. Credential writes that do occur
and generation pointer writes retain their existing durability.

Async generation reclamation could recreate a lease after teardown
unlinked it. The shared reclaimer now opens existing locks only. The fixed
binary passed ten consecutive cold runs without a teardown failure.

Build each revision with the static cross-compile command above. Alternate
the binaries on the reference host and run these commands for each sample.

```sh
"$bench_binary" -test.run '^$' -test.bench '^BenchmarkMaterializationHome$' -test.benchtime=10x
"$bench_binary" -test.run '^$' -test.bench '^BenchmarkCodexSetup$/^(cold|warm)$' -test.benchtime=10x
```

## Worker and CLI entry point refactor (2026-11-24)

Linux amd64, Intel i9-12900K, Go 1.27.3. Medians from five interleaved
runs of ten iterations per revision with the 32 MiB history fixture.
Both binaries used the same ext4 temporary directory and CPUs 2 and 4.

| Benchmark | Before (`093dbdca`, ms) | After (ms) |
| --- | ---: | ---: |
| `BenchmarkMaterializationHome` | 0.605 | 0.604 |
| `BenchmarkCodexSetup/cold` | 13.134 | 12.731 |
| `BenchmarkCodexSetup/warm` | 0.183 | 0.182 |

Build each revision with `go test -c -o "$bench_binary" ./internal/sync`.
Alternate the binaries five times with a shared `TMPDIR`.

```sh
taskset -c 2,4 "$bench_binary" -test.run '^$' -test.bench '^BenchmarkMaterializationHome$' -test.benchtime=10x -test.cpu=2
taskset -c 2,4 "$bench_binary" -test.run '^$' -test.bench '^BenchmarkCodexSetup$/^(cold|warm)$' -test.benchtime=10x -test.cpu=2
```

## Windows Codex generation selection (2026-11-24)

Linux ARM64 medians from five interleaved runs of ten iterations on `oracle-vm`
using the 32 MiB fixture and the benchmark commands above.

| Benchmark | Before (`2f7b1f55`, ms) | After (ms) | Budget (ms) |
| --- | ---: | ---: | ---: |
| `BenchmarkMaterializationHome` | 0.846 | 0.831 | <1 |
| `BenchmarkCodexSetup/cold` | 17.689 | 18.173 | <20 |
| `BenchmarkCodexSetup/warm` | 0.358 | 0.352 | <1 |

The Windows regression ran on `kyiv-win-1` with different temp roots for the
builder and launcher. The baseline Windows Codex benchmark could not report
timings because its cleanup tried to remove a lease held for the process lifetime.
