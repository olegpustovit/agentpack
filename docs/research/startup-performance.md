# Home-workspace startup performance

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
