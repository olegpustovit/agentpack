# Home-workspace startup performance

Measured on Linux ARM64, Go 1.26, 2026-10-30. Each result is the median of five runs with five iterations per run. These are local setup measurements, not model requests or harness process startup.

| Benchmark | Before | After |
| --- | ---: | ---: |
| Materialization fingerprint, home with 32 MiB runtime history | 26.76 ms | 0.362 ms |
| Fingerprint allocation | 17.74 MB | 0.165 MB |
| Codex rebuild, same history fixture | 33.06 ms | 11.59 ms |
| Codex verified warm setup | 0.172 ms | 0.175 ms |
| `ap` hub first frame (`BenchmarkHubFirstFrame`, list fixture 120×36) | — | 0.012 ms |

`BenchmarkHubFirstFrame` was repeated after the hub changes on 2026-11-03, with five runs of five iterations; median was 11.758 µs.

The materialization and preflight fingerprints previously recursed through entire `.claude` and `.cursor` directories. Launching from home therefore read global history and rejected Cursor's `projects/.../worker.sock` as nonregular configuration. A failed rebuild could not publish its generation, so the next launch repeated setup.

Both callers now select configuration files and authored component directories explicitly. Runtime state does not invalidate materialization. Selected configuration still uses the existing strict content fingerprint, including symlink handling and nonregular-file rejection. Cache integrity checks and launch validation are unchanged.

The fixed materialization fingerprint also completed against a real home profile in a median 38.53 ms; that profile contained about 1.7 GB under `.claude`, mostly runtime history. This read-only measurement is profile-specific and is not the synthetic budget.

Reproduce the controlled comparison on the same machine and filesystem:

```sh
go test ./internal/sync -run '^$' -bench 'Benchmark(MaterializationHome|CodexSetup)$' -benchmem -benchtime=5x -count=5
go test ./internal/hub -run '^$' -bench BenchmarkHubFirstFrame -benchtime=5x -count=5
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
