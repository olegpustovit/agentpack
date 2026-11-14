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
- P0.2: fork and toolchains pinned; static target confirmed in upstream build files.
- P0.3–P7 and Phase 1A: not implemented.
- Phase 0 gate: not reached; no performance or cross-platform claims yet.

## Source pin — P0.2

`superfly/cr-sqlite` default branch `main` resolved to
`8e32195704ccaea8508f7d378e11dc2dc0bb3d2e` on 2026-11-14.
`third_party/crsqlite.lock` records that immutable commit. Its checksum table
is intentionally empty until all target archives have actually been built.

Source evidence at that commit:

- `README.md`, Building: requires nightly Rust and documents static CLI linking.
- `core/Makefile`, `static` target: compiles the extension C sources and merges
  the Rust archive into `dist/crsqlite-<target>.a`.
- `core/rs/bundle_static/Cargo.toml`: `crate-type = ["staticlib"]`.
- `core/rs/bundle_static/rust-toolchain.toml`: `nightly-2023-10-05`.
- `core/src/sqlite/sqlite3.h`: SQLite **3.42.0**. The matching Go driver is
  `mattn/go-sqlite3 v1.14.17` (its bundled header also says 3.42.0); the latest
  driver v1.14.53 bundles 3.54.1 and cannot satisfy the proposal's exact-match
  requirement without changing the extension's headers. No driver has been
  added yet. This old SQLite baseline needs review at the Phase 0 gate.

### Replication semantics to resolve at the gate

The pinned fork's README explicitly says `db_version` is **per site** and
that `crsql_tracked_peers` bookkeeping is not supplied. The proposal assumes
a single cursor per peer and extension-owned tracked peers. A single scalar
cursor is insufficient for forwarding unrelated origin sites through a peer:
a site's lower-numbered changes could be skipped after another site's higher
version. Phase 4 must use origin-aware positions and preserve whole transaction
pagination. The Phase 0 sync experiment must cover these actual fork semantics;
the upstream protocol must not be assumed interchangeable with this fork.
