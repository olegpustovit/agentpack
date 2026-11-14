# Replicated state store implementation record

Specification: https://artifacts.microapps.space/a/7r76ssr0a7rd (version 10).
Baseline checkout: `0bd1c5b` (v0.13.9). No application state has been migrated.

## Baseline — P0.1

Measured 2026-11-14 on Linux amd64, Intel Core i9-12900K, Go 1.27.3.
These are medians of five runs of five iterations, using the existing 32 MiB
history fixture. This is not the Linux ARM64 reference host; these results
cannot certify its release budgets.

| Benchmark | Median | Five samples (ms) |
| --- | ---: | --- |
| `BenchmarkMaterializationHome` | 0.389806 ms | 0.356216, 0.402877, 0.389806, 0.374932, 0.449642 |
| `BenchmarkCodexSetup/cold` | 9.293943 ms | 10.432948, 8.546025, 9.020324, 9.293943, 9.421258 |
| `BenchmarkCodexSetup/warm` | 0.139241 ms | 0.139241, 0.151423, 0.128588, 0.131099, 0.152528 |
| `BenchmarkHubFirstFrame` | 0.018906 ms | 0.018906, 0.019112, 0.018361, 0.020308, 0.018796 |

Commands:

```sh
go test ./internal/sync -run '^$' -bench 'Benchmark(MaterializationHome|CodexSetup)$' -benchmem -benchtime=5x -count=5
go test ./internal/hub -run '^$' -bench BenchmarkHubFirstFrame -benchtime=5x -count=5
gh run list --workflow release.yml --limit 5 --json databaseId,status,conclusion,createdAt,startedAt,updatedAt,url
```

| Release baseline | Value |
| --- | --- |
| Last successful release workflow | [42070852362](https://github.com/olegpustovit/agentpack/actions/runs/42070852362) |
| Start | 2026-11-05 17:03:45 UTC |
| Finish | 2026-11-05 17:13:22 UTC |
| Elapsed (workflow timestamp span) | 577 s (9 min 37 s) |

GitHub's workflow timestamps include orchestration time, not just compilation.
The latest five successful runs were inspected; no release was triggered.

The shell initially had neither Make nor a C compiler. Validation uses an
isolated `nix shell nixpkgs#gnumake nixpkgs#gcc`; no project build settings
or user shell startup files were changed.

## Progress

- P0.1: complete; baseline benchmarks, release timing, and `make ci` passed.
  Validation command: `nix shell nixpkgs#gnumake nixpkgs#gcc -c bash -c "make ci"`
  (format, vet, full Go tests, race tests, build).
- P0.2–P7 and Phase 1A: not implemented.
- Phase 0 gate: not reached; no performance or cross-platform claims yet.
