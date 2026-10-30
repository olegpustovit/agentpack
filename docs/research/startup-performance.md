# Home-workspace startup performance

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
