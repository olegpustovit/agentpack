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

Validation now leaves nothing in the temp directory, and launches run the tidying in a goroutine beside the agent. Other commands wait for it at most 200 ms.

## Slow `add`, `lock` and `sync` (2026-11-04)

An `add` sat at `agentpack: add…` for over 3 minutes. Causes found:

- The shared HTTP client had no timeout, so one stalled GitHub or npm connection hung the command. It now needs response headers within 20 s and a whole request within 3 minutes.
- When codeload failed, the Git fallback cloned the whole history. It now fetches the one pinned commit at depth 1: `tanstack/router` went from 36.4 s to 5.5 s.
- A resolve downloaded the whole repository tarball once per subpath. It now reuses the last archive within one resolve.
- One command hashed each cache entry three times (resolve, plan, validate). `TreeDigest` now memoizes per process while every file keeps its path, mode, size and modification time, and never for a tree with a file changed in the last 2 s.
- `sync` waited for temp-directory tidying; it now waits at most 200 ms.
- Packages resolved one at a time. Each wave of the dependency queue now fetches up to six in parallel and reads their dependencies in queue order, so the lock is identical. Cold `lock` of the 11 packages: 21.5 s to 13.6 s.

Warm, on the Linux ARM64 host, with this repository's 11 packages (97 MB `impeccable` entry): `sync` 2.46 s to 1.95 s, re-`add` 5.06 s to 3.06 s, `lock` 0.17 s, launch 0.07 s. Most of what remains is rebuilding staging, which copies each hook package once per harness. Benchmarks after the change (medians of five): `BenchmarkMaterializationHome` 0.39 ms, `BenchmarkCodexSetup/cold` 14.9 ms, `warm` 0.15 ms.
