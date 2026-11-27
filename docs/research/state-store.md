# Replicated state store implementation record

Specification: https://artifacts.microapps.space/a/7r76ssr0a7rd (version 10).
Baseline checkout: `0bd1c5b` (v0.13.9). No application state had been migrated at that baseline.

Completed and deployed as v0.14.0 on 2026-11-15. The final publication and
deployment entry below supersedes the remaining gates in historical checkpoints.

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
- P0.3: host static library built and symbol checked; build-input tests and `make ci` passed.
- P0.4: static registration and Go store smoke test passed; `make ci` passed.
- P0.5: Make targets prepare the host library and enable cgo; `make ci` passed.
- P0.6: resumed with user authorization; all five target builds/link checks pass.
  Linux amd64, emulated Linux arm64, and native Apple Silicon smoke queries pass.
  Intel macOS and Windows runtime execution remain for the release smoke jobs.
- P0.7: replication, delete propagation, size and allocator measurements pass.
- P0.8: hidden product self-check passes natively, statically, and on the Mac.
- P0.9: libraries published/pinned; full five-platform build and runtime workflow passed.
- P0.10: release configuration and real-binary validation implemented and checked.
- P0.11: all platform/storage checks pass; warm CI completes in 551 s against the 697 s budget. Reference Linux ARM64 launch measurements remain open. P1.1–P1.7 and P1A.1–P1A.8 are implemented with the stderr/snapshot limits recorded below; P1A.9 onward remains.
- Phase 0 gate: platform and storage checks pass; reference Linux ARM64 startup measurement is still required.

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

## Host static build — P0.3

Built Linux amd64 musl with Zig 0.17.1 and nightly-2023-10-05.
Archive: `.build/crsqlite/8e32195704ccaea8508f7d378e11dc2dc0bb3d2e/linux_amd64/libcrsqlite.a`
(9.3 MiB). `nm --defined-only` confirms `sqlite3_crsqlite_init`.
`.build/crsqlite/lib` points to the pinned commit directory.

The script requires Bash with `mapfile` support, Python 3.11+ (`tomllib`), Go,
Git, Make, binutils (`ar`, `nm`), the pinned Zig and Rust toolchains, and
libclang for upstream bindgen. It checks the source SHA, exact compiler version,
SQLite header version and (once added) the Go driver's header version. Cargo
uses the source lockfile (`--locked`). Temporary source trees are removed.

First attempt failed because libclang was absent. Second attempt succeeded with:

```sh
nix shell nixpkgs#gnumake nixpkgs#gcc -c bash -c '
  export PATH="/tmp/agentpack-zig/zig-x86_64-linux-0.17.1:$PATH"
  export LIBCLANG_PATH="$(cat /tmp/state-libclang-path.txt)/lib"
  scripts/build-crsqlite.sh linux amd64
'
```

Here `/tmp/state-libclang-path.txt` holds the output of
`nix build --no-link --print-out-paths nixpkgs#llvmPackages.libclang.lib`.
The Zig archive was verified against the official download index SHA-256 before
extracting. These temporary paths describe this experiment, not a permanent
developer setup. Other targets and Go linking are not yet verified.

## Go linkage — P0.4

Added `mattn/go-sqlite3 v1.14.17` and the minimal `internal/state` store.
`sqlite3_auto_extension` registers the linked extension for every connection;
no shared library is extracted or loaded at runtime. The store currently uses
one connection and finalizes cr-sqlite before closing it. No production caller
opens this store yet.

`CGO_ENABLED=1 go test ./internal/state` passes on Linux amd64: SQLite reports
3.42.0 and `crsql_site_id()` returns 16 bytes. `make ci` also passes (including
race tests). Other platforms remain unverified.

The first Go link exposed Zig's default C sanitizer references in the archive.
The source builder now explicitly uses `-O2 -fno-sanitize=all` for C release
objects. Rebuilding and repeating the Go link succeeded. This removes the
unintended sanitizer runtime dependency rather than adding one to the product.
The old driver's C compiler emits a discarded-const warning; it is not hidden.

## Build prerequisites — P0.5

`make build`, `release`, `lint`, `test`, and `race` now prepare the pinned host
archive through `make crsqlite`. Existing archives are reused while source and
lock inputs are unchanged. `make crsqlite-build` explicitly rebuilds from source.
`CGO_ENABLED=1` is exported by Make. The download replacement remains P0.9.

`make ci` passed with the pinned host library. A C preprocessor check with the
archive temporarily removed confirmed the diagnostic names `make crsqlite`;
the archive was restored. The `internal/state` smoke test passes with the guard.

## Cross-target gate — P0.6: STOP

No work continues beyond this gate without human direction, per the runbook:
"STOP and report if any target fails to build or link. Do not work around it."

| Target | Static extension archive | Go linkage / execution |
| --- | --- | --- |
| linux/amd64 | Built; init symbol verified | Native GCC store smoke test passes; **Zig musl compile fails** |
| linux/arm64 | Built; init symbol verified | Not attempted after gate failure |
| darwin/amd64 | Compilation and archive assembly completed; **validation fails** | Not attempted |
| darwin/arm64 | Not attempted | Not attempted |
| windows/amd64 | Not attempted | Not attempted |

The Linux amd64 gate used a temporary Go program importing `internal/state`,
opening a database, and reading its site ID. The product has no state caller
until P0.8, so building only `cmd/agentpack` now would not prove extension
linkage. The explicit smoke link was run first to avoid a false positive:

```sh
CGO_ENABLED=1 GOOS=linux GOARCH=amd64 CC=/tmp/agentpack-zig-musl-cc \
  go build -trimpath -ldflags '-linkmode=external -extldflags=-static' \
  -o /tmp/ap-state-linux-amd64 ./.build/state-spike
```

The CC wrapper invokes the pinned Zig with `cc -target x86_64-linux-musl`.
The matching Go driver's old SQLite amalgamation does not compile against the
pinned Zig musl headers. Its complete compiler output follows:

```text
# github.com/mattn/go-sqlite3
sqlite3-binding.c:37644:42: error: use of undeclared identifier 'pread64'
 37644 |   { "pread64",      (sqlite3_syscall_ptr)pread64,    0  },
       |                                          ^~~~~~~
sqlite3-binding.c:37662:42: error: use of undeclared identifier 'pwrite64'
 37662 |   { "pwrite64",     (sqlite3_syscall_ptr)pwrite64,   0  },
       |                                          ^~~~~~~~
sqlite3-binding.c:37801:22: error: invalid application of 'sizeof' to an incomplete type 'struct unix_syscall[]'
 37801 |     for(i=0; i<sizeof(aSyscall)/sizeof(aSyscall[0]); i++){
       |                      ^~~~~~~~~~
sqlite3-binding.c:37810:22: error: invalid application of 'sizeof' to an incomplete type 'struct unix_syscall[]'
 37810 |     for(i=0; i<sizeof(aSyscall)/sizeof(aSyscall[0]); i++){
       |                      ^~~~~~~~~~
sqlite3-binding.c:37837:20: error: invalid application of 'sizeof' to an incomplete type 'struct unix_syscall[]'
 37837 |   for(i=0; i<sizeof(aSyscall)/sizeof(aSyscall[0]); i++){
       |                    ^~~~~~~~~~
sqlite3-binding.c:37854:16: error: invalid application of 'sizeof' to an incomplete type 'struct unix_syscall[]'
 37854 |     for(i=0; i<ArraySize(aSyscall)-1; i++){
       |                ^~~~~~~~~~~~~~~~~~~
sqlite3-binding.c:14847:38: note: expanded from macro 'ArraySize'
 14847 | #define ArraySize(X)    ((int)(sizeof(X)/sizeof(X[0])))
       |                                      ^~~
sqlite3-binding.c:37858:14: error: invalid application of 'sizeof' to an incomplete type 'struct unix_syscall[]'
 37858 |   for(i++; i<ArraySize(aSyscall); i++){
       |              ^~~~~~~~~~~~~~~~~~~
sqlite3-binding.c:14847:38: note: expanded from macro 'ArraySize'
 14847 | #define ArraySize(X)    ((int)(sizeof(X)/sizeof(X[0])))
       |                                      ^~~
sqlite3-binding.c:40513:11: error: type specifier missing, defaults to 'int'; ISO C99 and later do not support implicit int [-Wimplicit-int]
 40513 |     got = osPread64(id->h, pBuf, cnt, offset);
       |           ^
sqlite3-binding.c:37648:49: note: expanded from macro 'osPread64'
 37648 | #define osPread64 ((ssize_t(*)(int,void*,size_t,off64_t))aSyscall[10].pCurrent)
       |                                                 ^
sqlite3-binding.c:40642:17: error: type specifier missing, defaults to 'int'; ISO C99 and later do not support implicit int [-Wimplicit-int]
 40642 |   do{ rc = (int)osPwrite64(fd, pBuf, nBuf, iOff);}while( rc<0 && errno==EINTR);
       |                 ^
sqlite3-binding.c:37666:57: note: expanded from macro 'osPwrite64'
 37666 | #define osPwrite64  ((ssize_t(*)(int,const void*,size_t,off64_t))\
       |                                                         ^
9 errors generated.
```

The concurrently running Darwin source experiment used:

```sh
scripts/build-crsqlite.sh darwin amd64
```

It compiled and assembled `crsqlite-x86_64-apple-darwin.a`, then failed the
symbol check: the Linux GNU `nm` used by the builder reports Mach-O objects as
`file format not recognized`. This is a cross-format validation-tool failure,
not proof that macOS cannot link the extension. It still fails the required
gate. The temporary build tree was cleaned up and no Darwin archive was
installed. No workarounds were attempted after observing the gate failure.

### Verified state and next decision

P0.1–P0.5 are committed individually. `make ci` passed after P0.5, including
format, vet, all Go tests, race tests and the product build. Native amd64 GCC
links the extension and opens the database; fully static release linkage and
all-platform execution are **not proven**. No release was published or workflow
modified. No user data was opened or migrated.

Recommended next action for approval: continue Phase 0 by correcting musl
compiler compatibility and cross-format archive validation while retaining
option A and its exact SQLite header-match invariant, then repeat all five
target gates. Do not start any state migration or replication phase yet.

| Completed step | Commit |
| --- | --- |
| P0.1 baseline | `2ce2482` |
| P0.2 source pin | `31e6f31` |
| P0.3 static builder | `564f889` |
| P0.4 Go linkage | `b5ba396` |
| P0.5 Make prerequisites | `6a06d7b` |

## P0.6 resumed — compatibility results (2026-11-14)

The user authorized continuing with recommended fixes and supplied SSH access to
`snowbear@olegs-mac-mini`. The earlier STOP above records the original result;
it is superseded by this section. Future proposal gates are reporting points,
not requests for approval, per the user's explicit instruction.

### Changes

- Upgrade the Go driver to v1.14.53 / SQLite 3.54.1. Its headers now supply both
  the extension C build and Rust bindgen, with an exact version check against
  `crsqlite.lock`. This also supplies the JSONB required by Phase 1A. Go's module
  checksums verify the driver source. Upstream's old amalgamation is not used.
- Use native Xcode Clang and its SDK for Darwin. The script works with macOS's
  Bash 3.2 and uses native `nm -g`, rather than GNU-only switches. Xcode's
  libclang works; Homebrew libclang 23 generated incompatible Rust binding names.
- Compile each extension C file separately before archiving: Zig's COFF backend
  rejects the upstream Makefile's multiple-input `-c` command.
- Fully static Zig links add `CGO_LDFLAGS=-lunwind`; Rust's static allocation
  code references `_Unwind_Resume`. No shared unwind/SQLite extension is loaded.

| Target | Archive / Go link | Product binary | Smoke runtime |
| --- | --- | --- | --- |
| linux/amd64 | Pass, Zig musl | Static ELF; `--version` passes | `ok`, native |
| linux/arm64 | Pass, Zig musl | Static ELF | `ok`, QEMU aarch64 |
| darwin/arm64 | Pass, native Xcode | Mach-O; `--version` passes | `ok`, real Mac |
| darwin/amd64 | Pass, Xcode cross-compile on Mac | x86_64 Mach-O | Pending Intel runner |
| windows/amd64 | Pass, Zig GNU | PE32+ | Pending Windows runner |

The Mac runs macOS 27.0.1 (26A434), Apple Silicon, Xcode Clang 21.0.0.
Both `CGO_ENABLED=1 go test ./internal/state` and an independently linked smoke
executable opening the store pass there. Intel tests compile and link on it.
Rosetta is absent; it was not installed or its license accepted. macOS builds
will run on macOS runners rather than cross-linking from Linux.

Linux command pattern (use the appropriate target triple):

```sh
CGO_ENABLED=1 GOOS=linux GOARCH=amd64 \
  CC="zig cc -target x86_64-linux-musl" CGO_LDFLAGS=-lunwind \
  go build -trimpath -ldflags '-linkmode=external -extldflags=-static' \
  -o /tmp/ap-linux-amd64 ./cmd/agentpack
```

A separate smoke program imports `internal/state` and queries the site ID;
this proves linkage even before P0.8 adds a product caller. `file` confirms both
Linux executables are static. PE imports contain OS runtime DLLs and no SQLite
or crsqlite DLL. Product binaries for all five targets compile successfully.
`make ci` passes with the final source changes. Full all-target runtime and
release-time gates are still unproven; they remain required in P0.9–P0.11.

Only isolated files under `/private/tmp/agentpack-state-spike` were built on the
Mac; its project checkouts and installed agentpack state were not modified.
The pinned Rust toolchain was added to rustup. An unnecessary LLVM download
was cancelled once Xcode's libclang passed.

## Replication and storage measurements — P0.7

`internal/state/spike_test.go` contains `BenchmarkOpen`, `BenchmarkInsertEvents`
and `TestSyncTenThousand`. The fixture inserts 10,000 events with a 500-byte JSON
body in one transaction, applies all ten `crsql_changes` columns (including the
fork's `ts`) to an empty peer in one transaction, checks count and payload, then
deletes the source rows and checks that the replica is empty. Insert and delete
checks are separate subtests; each takes under 2 seconds even with musl (the
parent's aggregate is about 2.5 seconds). No network, sleeps or external data.

| Host / allocator | Open median | Insert 10,000 median | Sync 10,000 |
| --- | ---: | ---: | ---: |
| Linux amd64 GCC | 28.200 ms | 46.399 ms | 191.289 ms |
| Linux amd64 musl | 35.517 ms | 393.454 ms | 791.206 ms |
| Apple M1 macOS | 7.344 ms | 99.228 ms | 203.277 ms |

Benchmarks: five runs at `-benchtime=1x`; sync is the median of five except GCC,
which is a single fixture run. The Linux reference here is the same amd64 host
as P0.1, not the ARM64 release-budget reference machine. Mac tests ran on the
user's physical Mac. Musl is measurably slower for these allocation-heavy
operations but still passes the stated gate.

| Size | Source | Replica |
| --- | ---: | ---: |
| Raw JSON | 5,000,000 bytes | 5,000,000 bytes |
| Database after insertion | 5,795,840 bytes | 5,795,840 bytes |
| Database after deletion | 5,795,840 bytes | 5,795,840 bytes |

The ratio is **1.1592×**, below the 3× gate. Deletion leaves reusable SQLite
pages rather than shrinking the file; deleted replicated events are gone on
both nodes. This experiment uses the minimal P0.4 opener's default journal
settings; Phase 1 must remeasure WAL / FULL with its actual connection model.

Reproduce:

```sh
go test ./internal/state -run TestSyncTenThousand -v
go test ./internal/state -run '^$' -bench 'Benchmark(Open|InsertEvents)$' -benchtime=1x -count=5
CGO_ENABLED=1 CC='zig cc -target x86_64-linux-musl' CGO_LDFLAGS=-lunwind \
  go test ./internal/state -run TestSyncTenThousand -v \
  -bench 'Benchmark(Open|InsertEvents)$' -benchtime=1x -count=5
```

`make ci` passes, including the new tests with the race detector. Final Mac
smoke executable dependency inspection (`otool -L`) shows only system libraries
and no SQLite/crsqlite dylib; its Intel smoke executable also links successfully.

## Product self-check — P0.8

Hidden Cobra command `ap state-selfcheck` creates a temporary database, creates
a CRR, writes a row, reads it through `crsql_changes`, finalizes the extension,
closes the database and removes its temporary directory. It prints exactly `ok`.
A CLI test checks its output, exit status and hidden flag.

`make ci` and `bin/ap state-selfcheck` pass. The fully static Zig musl product
binary and the native Apple Silicon product binary also print `ok` for the
command. This now verifies the extension in the actual product, not a separate
spike binary. Other launch paths do not call the self-check or open a store.

Startup setup benchmarks repeated after the product import (same host/fixture
and five runs of five iterations as P0.1):

| Benchmark | Median |
| --- | ---: |
| `BenchmarkMaterializationHome` | 0.334202 ms |
| `BenchmarkCodexSetup/cold` | 6.871348 ms |
| `BenchmarkCodexSetup/warm` | 0.145098 ms |

These are amd64 setup measurements; ARM64 reference-host release budgets and
full warm release duration still require their own evidence.

## Release bootstrap — P0.9 in progress

The release pipeline now separates source-pin bootstrapping from normal builds.
A missing `crsqlite-<commit>` release builds the five libraries (Darwin on a Mac),
publishes only a prerelease marked never-latest, prints the checksums, and stops
until those digests are recorded in the lock. This is the proposal's two-commit
bootstrap; the initial expected checksum failure is not a product release.

Normal jobs fetch checksum-verified archives without Rust. The downloader tests
cover verified-cache reuse, missing and wrong hashes, symlinks and traversal;
verification failure preserves the previously installed library. Product builds
use per-target Go caches and the pinned Zig or native Xcode toolchain.

The full GoReleaser job runs on macOS: a Linux cross-link experiment confirmed
that Zig cannot supply the product's CoreFoundation/Security frameworks or
libresolv without an Apple SDK. This uses the proposal's native-Mac fallback;
no SDK is copied or redistributed. GoReleaser OSS remains sufficient.

A build/snapshot job runs before five real-target smoke jobs (ARM Linux under
QEMU), and publishing depends on all five. A post-build hook compares each
publishing binary against the exact archive that passed smoke tests; mismatches
refuse publication. The source branch carries no product release tag.

Local checks before bootstrapping: downloader/publishing regressions pass,
`actionlint` is clean, and `goreleaser check` validates the configuration. A Linux
single-target GoReleaser static build also passes. The archive checksums and
Make's switch from source preparation to downloads are deliberately pending the
first successful library publication; P0.9 is not complete yet.

### Published library pin and normal fetch path

[Bootstrap run 43601266843](https://github.com/olegpustovit/agentpack/actions/runs/43601266843)
built all five source targets successfully and published the six expected
[prerelease assets](https://github.com/olegpustovit/agentpack/releases/tag/crsqlite-8e32195704ccaea8508f7d378e11dc2dc0bb3d2e).
It then failed at the planned missing-checksum check. Those generated archive
SHA-256 values are now recorded in `third_party/crsqlite.lock`. `make crsqlite`
and its dependent targets now fetch and verify, without Rust or Zig on a normal
host-native build. The verified archive is cached and checked on repeat builds.

`make ci` passes using the published Linux archive. Added a guard for a Go/cgo
cache pitfall: link paths contain the actual source pin, and a test requires
all five directives to match the lock. A future source-pin bump must update
those directives, changing Go's link action instead of reusing a binary linked
against the previous C library. No build caches are cleared.

The native Mac fixture fetched all five published libraries and is running the
full `make release-check`, including UI staging and GoReleaser snapshot builds.
Full platform smoke results and warm workflow timing remain pending; this is
still an ongoing P0.9 implementation, not a completed Phase 0 gate.

The first checksum-pinned run (43602091356) was **not accepted as evidence**:
it reported success while skipping Windows, GoReleaser and smoke jobs. GitHub's
implicit `success()` condition propagated the intentionally skipped source-build
ancestor. Downstream gates now explicitly use `always()` plus the required
predecessor's successful result. A configuration regression checks that the
cached-library path cannot silently skip these gates. `make ci` and actionlint
pass after the correction; the complete workflow is being rerun.

Native `make release-check` on the supplied Mac passed: UI staging, all Go tests,
five product builds, archives, installers, checksums and Homebrew cask generation.
The cold local snapshot took 6m56s, not a warm GitHub workflow measurement.
Run 43602401866 correctly reached the Windows gate and caught an open-file
rename in the downloader (WinError 32). Installation now closes/fsyncs its
staging file before atomic replacement. Existing download regression tests and
`make ci` pass; the real Windows gate is being repeated.

## Release validation — P0.10

Release configuration tests reject a missing compiler, disabled cgo and missing
Linux static flags. All five archive checksums must be present and well formed,
and both source-build and runtime smoke matrices must include every target.
The existing installer/header checks remain unchanged for product releases;
the documented `crsqlite-*` build-input exception was added during bootstrap.

The negative check was performed against the real file: temporarily changed
`.goreleaser.yaml` to `CGO_ENABLED=0`, ran the configured-build test, observed its
`cgo disabled` failure, and restored the file in a `finally` block.

`TestReleaseBinaryDependencies`, with `AGENTPACK_RELEASE_DIST` set, inspects all
five actual snapshot binaries. ELF files must have neither an interpreter nor
dynamic libraries; Mach-O/PE files must not import SQLite/crsqlite. Go's PE
`ImportedLibraries` is a TODO returning no libraries, so Windows inspection uses
Xcode LLVM's complete import table (GNU objdump on other build hosts), including
ordinal-only imports, and refuses an empty inspection. This avoids a false
positive from an incomplete standard-library API.

All five real snapshot binaries passed this inspection on the Mac. The Make
`release-check` target and snapshot workflow now run it explicitly; ordinary
Go test runs skip it when no snapshot directory is supplied. `make ci` and
workflow lint pass. The warm Mac snapshot took **53 s**, compared with 6m56s
cold on that same machine. This is not substituted for a warm GitHub workflow
measurement or for the Linux ARM64 reference-host startup budgets.

Run 43602899053 passed the complete Windows package, launch and PowerShell
suite after the atomic-download fix; its GoReleaser and smoke stages are still
running. Full all-target runtime completion has not yet been claimed.

### Exact publishing proof and deterministic Windows metadata

A publishing rehearsal on the Mac rebuilt all five binaries and compared each
to a previously staged archive. It correctly rejected Windows: the PE timestamp
and derived build identifier varied between links. Setting the standard
`SOURCE_DATE_EPOCH=0` in GoReleaser's build environment fixes this with the pinned
Zig toolchain. Two complete snapshots then passed the active byte-for-byte
verification for **all five targets** (51 s staging, 46 s verified rebuild).
The release configuration test now requires the deterministic environment.

Approved artifacts are downloaded under `.release-assets/approved`, which is
ignored. A regression checks this against Git and verifies that the hook's
input directory matches the downloader. Keeping these inputs ignored preserves
the same VCS build metadata between testing and publishing. No product release
was published during these rehearsals.

Source installation instructions now state the C compiler/Python prerequisites,
`make crsqlite`, explicit cgo, and Windows/native-Mac release compiler settings.
`make ci`, negative configuration checks, all-target binary inspection and
workflow lint pass. The real five-platform runtime smoke jobs remain in flight.

## Phase 0 gate audit — P0.11

[Run 43602899053](https://github.com/olegpustovit/agentpack/actions/runs/43602899053)
completed successfully with the complete Windows suite, the native-Mac
GoReleaser job, and **all five** runtime smoke jobs. Only the expected source
rebuild and product publishing jobs were skipped. This is accepted runtime
evidence, unlike the earlier false-green cached-library run.

| Gate | Required | Evidence | Result |
| --- | --- | --- | --- |
| Targets build/link and real extension smoke | All five | Successful five-target workflow above; Linux arm64 under QEMU, both macOS architectures and Windows on their own runners | Pass |
| Linux static linkage | No interpreter or shared dependencies | Both ELF headers checked; static smoke binaries execute | Pass |
| Extension runtime files | None | Mach-O and PE imports inspected; source library linked statically | Pass |
| 10,000-event sync | <2 s | Musl median 0.791 s | Pass |
| Open | <50 ms | Musl median 35.517 ms | Pass |
| Storage | <3× raw JSON | 1.1592× | Pass |
| Warm release duration | Baseline 577 s + at most 120 s | Run 43613015251: 660 s; newer 43616405381: 728 s, all checks pass but duration exceeds 697 s | **Investigating latest warm-run regression** |
| Reference Linux ARM64 launch budgets | Fingerprint <1 ms, cold <20 ms, warm <1 ms | amd64 after-change values 0.334/6.871/0.145 ms; QEMU is not reference-host performance evidence | **Reference host measurement still required** |

Current source (6eca5aa) additionally enables deterministic Windows timestamps
and strict binary dependency tests. Its workflow is running; the prior runtime
proof above does not substitute for this latest run. The five-target byte-exact
publishing rehearsal passes locally on the Mac with this configuration.

Per the user's instruction to continue with recommended assumptions, unresolved
performance measurements remain explicit release requirements while implementation
continues into Phase 1. No product release or user-data migration is performed
on the strength of an unverified gate.

## Durable opener — P1.1

`state.Open(home)` now owns `<home>/state.db`, secures it to 0600, rejects a
nonregular/symlink database, and escapes arbitrary home paths in the SQLite URI.
It provides one serialized writer and a separate query-only reader pool. Every
connection uses WAL, FULL synchronization and a 5 s busy timeout.

A driver connection wrapper finalizes cr-sqlite on **each** connection's Close,
including connections retired by `database/sql` before Store.Close. It preserves
the underlying driver's context/query interfaces through embedding. The CLI
self-check and benchmark fixtures now use the home-based opener contract.

Tests cover reopen persistence, all three pragmas on both pools, database/WAL/SHM
0600 permissions, read-only enforcement, readers seeing committed data while a
writer transaction is open, retirement of pooled connections, two Store values
writing concurrently without lost writes or lock errors, and symlink rejection
without modifying the target. `make ci` passes; targeted store/CLI tests also
pass on the Mac. No gateway has been switched to this store yet.

## Atomic migrations — P1.2

Embedded numbered SQL migrations run in filename order, one transaction per
file, maintaining one validated `schema_meta` row. `SchemaVersion()` exposes
the supported version. `BEGIN IMMEDIATE` serializes version checks across
processes, and a failed file rolls back its DDL and version update. A database
from a newer binary is refused without changing its schema version.

The new concurrent-fresh-open test exposed extension initialization occurring
before the Go driver's DSN busy timeout was installed. The static initializer
now sets SQLite's busy timeout before invoking cr-sqlite; repeated concurrent
opens and migrations then pass without retries or extra lock files.

Fresh/repeated migration byte-stability, rollback, successful retry, concurrent
startup and future-schema refusal tests pass. `make ci` passes. No gateway
production path opens the store yet; existing user state is still untouched.

## Launch-path guard — P1.3

The existing cold/warm launch-setup regression now asserts that cold setup
creates no `state.db`. It then puts an intentionally invalid database marker in
the home and proves verified warm setup neither opens SQLite on it, changes its
bytes/permissions/mtime, nor invalidates the fast path. This checks setup before
spawning a gateway or worker, since those processes are explicitly allowed to
open state after a launch. No runtime database is added to fingerprints.

Five runs of the existing 32 MiB history fixture on the same amd64 host:

| Benchmark | Median |
| --- | ---: |
| `BenchmarkMaterializationHome` | 0.350201 ms |
| `BenchmarkCodexSetup/cold` | 7.134586 ms |
| `BenchmarkCodexSetup/warm` | 0.136396 ms |

The guard and `make ci` pass. These remain amd64 measurements, not a claim about
the original ARM64 reference machine.

## Gateway ownership — P1.4

`ServeGateway` opens state only after acquiring its process lock and validating
the public origin, retains the store on `Gateway`, and closes it on shutdown.
Close errors are joined with the returned server result. The sharing loop exits
before any work when its context is already cancelled.

Lifecycle tests create and stop a gateway without external services, find the
latest persisted schema, prove that WAL/SHM handles were released, and verify
that a lock-denied second gateway never touches `state.db`. `make ci` passes.

Latest Phase 0 validation run 43605315971 passes the full Windows suite,
GoReleaser, binary dependency checks and every platform smoke. Its total 802 s
exceeds the 697 s warm-workflow threshold. Step timing identifies 131 s spent
running all Go tests before UI staging, followed by the existing full Go test
hook after UI staging. That redundant first suite can be removed while retaining
full coverage in GoReleaser's hook; the performance gate remains open until the
corrected workflow is measured.

The duplicate pre-staging full suite is removed. The complete suite still runs
once in GoReleaser's before hook after UI staging, and Windows retains its own
full platform suite. A regression enforces this ordering and prevents the
workflow from adding a duplicate. Local `make ci` and workflow lint pass; the
697 s workflow budget is unchanged and awaits the next measured run.

## Limits in SQLite — P1.5

Migration 0002 creates per-node/per-harness limit rows. The gateway loads and
atomically replaces its own snapshots in SQLite, retaining the existing stale
read/background-refresh behavior and probe coalescing. Other nodes' rows are
preserved. The disposable legacy `network/limits.json` is removed on gateway
start without import, as required. Storage failures produce an unavailable
response instead of silently falling back to another store.

Tests cover restart persistence, per-node isolation, replacement of a node's
obsolete harness rows, legacy-cache removal and nonblocking stale reads. Channel
synchronization replaces the old test's sleep/poll loop. Migration rollback tests
now derive their next migration number from the embedded schema. `make ci`
passes, including the race detector.

There is one explicit transitional key: local-only limits use `local` before
mesh enrollment. The code marks the required P4.1 rekey to the stable tailnet ID
before CRR conversion; this is outstanding work, not the final mesh identity.

## Sharing settings in SQLite — P1.6

Migration 0003 adds node_settings. Sharing reads and writes use the gateway's
existing store rather than reopening SQLite per discovery/auth request. Explicit
CLI sharing commands own a short-lived store; launch setup remains unchanged.
The legacy network/sharing.json import was removed before release; a missing
row means sharing on. The local node key shares the limits P4.1 rekey requirement.

Tests cover defaults, enabled/disabled settings, allow validation and restart persistence.
`make ci` passes. Workflow 43610347558 caught a Windows-only fixture error:
question marks are illegal in Windows directory names. The URI-escaping test now
uses spaces, Unicode, hash and ampersand on every platform, plus question marks
on Unix. The warm workflow budget still awaits a complete successful run.

## OAuth in SQLite — P1.7 and Phase 1 verification

Migration 0004 adds local oauth_clients (hashed key and client metadata) and
oauth_tokens (hashed key and grant fields), with expiry/family indexes. The
OAuth code uses the gateway-owned state store, SQL transactions, indexed pruning
and family revocation. There is no separate OAuth database handle or forwarding
Close method. Gateway startup deletes legacy network/oauth.db; clients must
register again, as specified for this pre-release migration.

Writer transactions use BEGIN IMMEDIATE, taking the write lock before reading a
one-use grant. Reader transactions remain deferred/query-only. Existing OAuth
PKCE, consent, refresh rotation/replay, revocation and restart tests pass. A new
test redeems one code concurrently through two Store connections and proves
exactly one succeeds. Transaction commit/rollback and legacy-file removal are
covered. `make ci` passes, including the race suite.

On the real Apple M1 Mac, all state/network/CLI tests pass. No installed state
was touched: tests/builds use the isolated /private/tmp checkout and temp homes.
The 10,000-event WAL fixture now checkpoints before reporting database size;
otherwise measuring only state.db would misleadingly exclude WAL pages.

| Phase 1 measurement | Linux amd64 | Apple M1 arm64 macOS |
| --- | ---: | ---: |
| Fresh open, median of five | 47.502 ms | 6.003 ms |
| Insert 10,000 events, median of five | 54.491 ms | 90.747 ms |
| Sync 10,000 events | 155.278 ms | 189.900 ms |
| Checkpointed source / replica size | 5,849,088 bytes each | 5,849,088 bytes each |
| Size after delete/sync | 5,849,088 bytes each | 5,849,088 bytes each |

The raw payload is 5,000,000 bytes: current ratio 1.1698x. New tables account for
the small fixed difference from the Phase 0 database. Reproduction uses the same
state benchmark and TestSyncTenThousand commands above, now with WAL/FULL and
all four migrations.

Five repeated launch measurements first showed shared-host timing noise
(fingerprint 0.373159 ms, cold 12.944158 ms, warm 0.258813 ms). A second set with
unchanged code measured fingerprint 0.342552 ms, cold 6.925838 ms and warm
0.134045 ms. Both sets meet the numeric budgets on this amd64 host; the original
Linux ARM64 reference host remains unmeasured. Launcher no-state-access checks
continue to pass. Warm full-workflow timing is still pending.

## Observability core — P1A.1

The new internal/obs package owns context events, W3C traceparent validation and
child spans, error-chain/caller-stack capture, normalized fingerprints, bounded
redaction, and a nonblocking 4,096-event queue with an exact dropped counter.
Events carry build/platform/process/schema metadata. End emits once and caps the
encoded event at 32 KiB; nested attributes stop at depth six. Headers use an
allowlist. Credential, cookie, environment, prompt and body attributes are
removed, and credential-shaped string values are redacted.

The Library credential check moved into obs rather than being copied. All
registry callers use that one policy. Ordinary ASCII checks avoid regex work
where possible; Unicode keys retain the original regexp folding. Tests caught
and fixed a GitHub-token candidate check that was too narrow. Tests cover
trace propagation/invalid headers, grouping, secret exclusion, size/depth caps,
concurrent attribute updates and a stalled queue (4,096 retained, 5,904 dropped
from 10,000 events). The Library capture/regression tests and `make ci` pass.

BenchmarkEvent (amd64, three runs): 8.852 / 8.506 / 8.443 microseconds per event,
including metadata, redaction and queue delivery. This is the core event cost,
not yet the P1A.4 HTTP middleware measurement. No runtime sink is connected yet;
day files, spool files and request instrumentation follow in P1A.2–P1A.4.

Workflow 43611170066 now passes Windows, the complete GoReleaser snapshot and
all five runtime smoke checks. Total duration is 937 s, still above 697 s.
Windows took 396 s and then the Mac build took 456 s: these independent checks
are currently serialized. The next pipeline adjustment moves the Windows gate
to publication so both checks can run concurrently, retaining all coverage.

The release DAG now runs Windows and GoReleaser concurrently after the verified
library gate. Publication explicitly requires both Windows success and all
five smoke successes. Regression checks validate those dependencies/conditions
and prevent reintroducing the serial build dependency. `make ci` passes.
The unchanged 697 s budget awaits a measured run of this DAG; no timing pass is
claimed from estimated savings.

## Plain SQLite day store — P1A.2

internal/obs/store.go owns one bounded queue and one writer. It writes UTC day
files with WAL/NORMAL, JSONB attributes, typed filter columns and partial
fingerprint/session indexes; batches commit after 250 ms or 256 events. Identical
stacks use one hash row. A trace/span/node uniqueness key makes replay idempotent
for the upcoming spool importer. Flush and Close drain the queue and surface
persistence failures; a redacted stderr fallback reports the first failure.

Startup/hourly sweeps delete expired day files and orphaned sidecars. The 256 MiB
budget counts the database and WAL and conservatively reserves index/WAL pages;
ordinary events are dropped at the limit. Errors and aggregate drop reports are
retained as required. Clock, intervals and the test size limit are injectable.
Writer metadata is used for drop reports, rather than attributing an aggregate
to whichever source event happened to come first.

The process-wide static extension initializer now recognizes the per-database
agentpack_plain URI flag. Log connections therefore load neither CRR tables nor
extension state, while concurrent state connections retain cr-sqlite. A test
proves this separation. Future archive/cache connections must use the same flag.

The existing private-directory helper moved, with its Windows inheritable-DACL
test, from network to its shared owner paths. Every caller now imports paths;
there is no forwarding alias. State and log directories reuse it. Log database
files are 0600 and nonregular/symlink database paths are refused.

Tests cover UTC rollover with a non-UTC clock, start/hourly retention and orphan
sidecars, 10,000 durable tiny events, repeated stacks, replay deduplication,
JSONB/plain-file storage, file permissions, cap/drop/error behavior and failed
writes preserving a symlink target. `make ci` passes; the Mac passes the
state/obs/network/CLI/paths/hub race suites. The replay-tail test was also made
fully in-memory: its former live child could append READY after the test
installed its fixture. The new test passes 20 race-enabled repeats and now also
runs on Windows.

Warm workflow 43613015251 (8588afa), from 15:58:19 to 16:09:19 UTC, passes in
660 s against the unchanged 697 s budget. It includes the full Windows suite,
GoReleaser and every platform smoke. The remaining performance evidence gap is
the original Linux ARM64 reference-host launch benchmark, not cross-platform
runtime compatibility.

## Process spool and gateway ingestion — P1A.3

The spool sink queues up to 4,096 events and appends one capped/redacted JSON
line with one Write call to a process/PID/minute NDJSON file. Empty construction
and shutdown create no log directory. Each line's file is closed immediately:
an idle process must not keep Windows from deleting an aged spool. File names
are validated and directories retain the shared private-permission handling.

The gateway now owns its day store and ingests spool minutes at least two
minutes old on startup and once a minute. It joins its background loops before
closing stores. Ingestion deletes source files only after successful commits;
bulk Import applies backpressure, while normal live Write remains nonblocking.
The existing trace/span/node key makes a commit-before-unlink replay idempotent.
A transient write failure leaves the source and can be retried; Flush reports
failures since its previous barrier, while Close retains the lifetime failure.

The reader bounds each line, skips/counts malformed, oversized, unterminated and
invalid-trace entries, and continues reading later valid lines. Spool retention
removes the oldest files first above 16 MiB. Tests cover two process spools,
the two-minute boundary, all valid events, crash replay, damaged last lines,
oversized records, the directory cap, safe process names, filesystem-free empty
spools, and failed-import recovery. `make ci` and the Mac obs/network race
suites pass. Full five-platform workflow 43614669421 also passes for P1A.2.

Runtime HTTP instrumentation follows in P1A.4. Workers/CLI begin emitting their
own events in P1A.6; their sink and ingestion paths are now ready.

## HTTP observation and propagation — P1A.4

Every gateway request now gets a validated/generated trace ID and response trace
header, with one event at completion. Route names are matched patterns; request
paths and bodies are not stored. Observations include status, response body byte
count, elapsed time, the verified actor, node routing and session ID when the
route provides one. Validation/authorization failures remain info; server errors
and panics are errors. Health checks return a trace header without storing an
event. SSE and WebSocket events end only when their connection handler finishes.

Panics before headers get a generic 500 and a caller stack. A panic after a
response has started aborts the incomplete response rather than appending a
fake successful body; hijacked connections are explicitly closed. The gateway
continues serving later requests. A real gateway/day-file test verifies one
persisted panic, a healthy later response and absence of the cookie.

Response-writer interface preservation uses github.com/felixge/httpsnoop v1.1.0
([upstream rationale](https://github.com/felixge/httpsnoop#why-this-package-exists)),
retaining SSE, WebSocket, ReaderFrom and response-controller behavior. The small
metrics hooks count body bytes without retaining them. go mod tidy also removes
the obsolete go-sqlite3 v1.14.17 checksum entries.

Peer and worker requests carry traceparent, including a trace-only CLI context.
The peer proxy now uses ReverseProxy.Rewrite so hop-by-hop removal cannot erase
the newly set trace header. It still removes credentials/cookies, and preserves
one gateway trace response header rather than duplicating an upstream header.
Tests cover corrected peer traces even with a hostile Connection header, worker
propagation, trusted actor/route metadata, privacy, panic recovery/late abort,
and one event per SSE/WebSocket closure.

`make ci` and the Mac obs/network race suites pass. BenchmarkMiddleware uses the
live asynchronous day store, not a no-op sink. Medians of three one-second runs:

| Host | Bare handler | Instrumented | Added cost | Budget |
| --- | ---: | ---: | ---: | ---: |
| Linux amd64 i9-12900K | 0.1651 us | 17.812 us | 17.6469 us | <20 us |
| macOS arm64 Apple M1 | 0.1891 us | 19.341 us | 19.1519 us | <20 us |

Reproduce with `go test ./internal/network -run '^$' -bench '^BenchmarkMiddleware$'
-benchmem -benchtime=1s -count=3`.

P1A.3 workflow 43616405381 passes all five runtime platforms but takes 728 s,
above 697 s. GoReleaser alone took 8m54s, including roughly four minutes for
Linux/Windows builds after the Darwin builds finished. All five Go caches hit.
The pipeline does not cache Zig's global compilation cache; recompiling target
runtimes on each runner is the next hypothesis to verify. No timing waiver or
increased budget is assumed.

## Shutdown verification follow-up

The compiled concurrent-launch integration test sometimes removed its temporary
home after the stop RPC acknowledged shutdown but before the gateway flushed
its final HTTP event. Cleanup now waits for the gateway's existing process lock
to be released before removing files. This preserves the API's explicit
"stopping" acknowledgement. Twenty repeated integration runs and `make ci`
pass; no new production stop protocol or arbitrary cleanup delay is added.

## Zig runtime cache follow-up

Both the snapshot and publication jobs now restore a shared, explicit
ZIG_GLOBAL_CACHE_DIR before building. Its cache key includes host OS/architecture
and the pinned Zig version. The existing per-target Go caches remain separate.
A regression verifies that restoration precedes each GoReleaser action and that
the compiler uses the restored path. `make ci` passes.

An isolated Apple M1 experiment compiled a tiny C program for x86_64-linux-musl
with static linkage and libunwind, using a new temp global cache: 31.10 s cold,
0.14 s warm (same pinned Zig 0.17.1). This confirms that target-runtime cache
state materially affects cross-linking cost. The pending CI run seeds this new
cache; a subsequent warm workflow must still prove the unchanged 697 s budget.
Neither these timings nor a successful platform smoke is treated as that proof.

## Issues, exact counts, sampling and regressions — P1A.5

Migration 0005 adds the requested issues and per-node issue_counts tables plus
small, local issue_log_cursors. Gateway-owned day stores aggregate their own
node's errors. The first 20 per fingerprint/node/hour are retained, then one in
50; every accepted occurrence contributes to the count. New fingerprints get a
redacted JSON sample capped at 16 KiB. Older late-arriving events can move
first_seen earlier without changing the original sample.

Day-file meta counters and per-spool-file processed-line cursors commit in the
same transaction as sampled events. Canonical count publication applies only the
unpublished delta per day/version, atomically with its state cursor. This avoids
one permanent receipt per error, prevents spool retries from inflating sampled
counts, and recovers committed counters after interrupted publication. Raw day
retention waits for pending counts; it never forces an early count update just
to delete a file. Local publication cursors are pruned with the retained days.

The writer accumulates in memory and publishes at most once per 30 seconds per
fingerprint/node, with a final shutdown flush. A resolved issue reopens with
regressed=1 only for a new occurrence from the fix version or newer; unparseable
versions count as newer. Older builds still add to the count. Version deltas
prevent a previously seen newer build from reopening an issue on an older
occurrence. Ignored issues remain ignored. The existing semver package is reused.

Tests prove: 1,000 identical errors yield one issue, exact count 1,000 and 39
stored samples; hourly sampling resets; all version-regression cases; replay of
an already committed spool; restart recovery after failed state publication;
retention across the coalescing boundary; earlier first_seen; and a real gateway
panic incrementing its canonical issue count. `make ci` and the Mac
obs/state/network race suites pass.

Oversized event/sample capping now preserves the mandatory build/platform/schema
and process attributes and a short first error, with a truncation marker. A
regression checks both size limits. Truncated strings detach their backing
storage so a bounded queued event cannot retain a discarded huge string.
Middleware benchmark added medians remain within 20 us: Linux 17.6976 us and
Apple M1 19.2602 us (three one-second runs; local first run shared CPU with CI).

The cache-seeding workflow 43619677112 passes every platform in 915 s. This run
also seeded the new dependency-cache keys; it is not the required warm timing.
The next unchanged-toolchain run must still verify the 697 s warm budget.

## Warm release timing verified

Workflow 43622311770 at c1abf91 passes the Windows suite and all five runtime
smokes in 551 s (17:39:43–17:48:54 UTC), below the unchanged 697 s budget.
GoReleaser and Windows remain concurrent; the Zig global cache is restored.
This closes the warm-pipeline timing check, not the separate Linux ARM64
reference-host setup benchmark requirement.

## CLI failures and background panic recording — P1A.6, partial

Each CLI invocation creates or inherits a validated trace, with no filesystem
access on that path. The existing Fang error boundary emits exactly one CLI
failure event to a spool, including the command path, status, redacted arguments
and error chain. Successful commands emit nothing. Harness arguments are omitted
because they may contain prompts. Existing credential-argument redaction is
exported and reused rather than copied. A detached worker receives its parent's
trace ID through its own AGENTPACK_TRACE environment entry.

obs.Go snapshots the process-owned sink, records a panic with its stack, attempts
a bounded two-second flush and re-panics. The gateway's event polling, peer
relays, sharing listener and supervised background loops now use it. Request
contexts retain their existing sinks and trace propagation.

Tests cover one failure versus no successful event, credential and harness-prompt
exclusion, child trace inheritance, invalid trace IDs, and a subprocess proving
that a panic both reaches disk and still terminates the process. A compiled
foreground harness launch is exercised cold and warm with the gateway disabled;
neither logs/ nor state.db appears. Disabling the gateway isolates the launch
from the independent HTTP events the gateway is required to persist.

The full local checks exposed the same cleanup race in other integration tests:
the gateway-stop RPC acknowledges shutdown before the last log flush. All cleanup
callers now reuse the existing process-lock wait, and the shared CLI test helper
uses the actual overridden AGENTPACK_HOME. The three affected integration cases
pass ten repeated runs, and make ci passes. The Mac obs/CLI/network/sync race
suites pass. No production shutdown handshake or sleep was added.

The final Mac CLI/integration race run also passes. Five isolated local Linux
amd64 setup runs (one-second iterations, the existing 32 MiB history fixture)
give medians of 0.302059 ms for MaterializationHome, 7.509646 ms cold Codex setup
and 0.117226 ms warm. These stay within the numeric setup budgets; the earlier
same-host medians were 0.342552/6.925838/0.134045 ms. They do not establish the
outstanding Linux ARM64 reference-host requirement.

P1A.6 remains open: worker lifecycle events, ACP turn summaries and error-state
snapshots still need implementation. The physical Mac validation uses an
isolated temporary checkout; no installed application data is changed.

## Worker lifecycle and ACP turn recording — P1A.6

Workers now own a spool sink and carry the release version explicitly from the
CLI. Start, explicit stop, restart and unexpected exit events include session
identity, status and error chains; failed startup is also recorded. Restart
retries reuse the existing operation cache and emit one event per operation.
Repeated stop calls still wait for termination without duplicating the event.
Intentional forced stops are distinguished from unexpected exits. Trace IDs
survive worker IPC and the native child environment, including after restart.

ACP adapters retain only their last 8 KiB of separate stderr. The whole tail is
checked for credential shapes and passed through Redact, then stored as two
chunks because individual attribute strings cap at 4 KiB. Exit codes are retained
atomically. Explicit cleanup does not report an unexpected exit. Native terminal
workers keep stderr_unavailable="terminal streams are merged": their PTY merges
chat/output with stderr. Capturing that replay would violate the no-chat rule;
separating the descriptor would change the terminal behavior. A future terminal
backend with a separate diagnostic stream can remove this limit.

One acp.turn event spans the broker's actual prompt operation, including waits
for tools and permissions. Append-time counters retain first/last seq and counts
when the bounded replay tail evicts earlier events. Only known stop reasons are
accepted; arbitrary response fields and all prompt/reply/tool content are
excluded. Cancellation records a failed turn. A second overlapping prompt is
rejected while the first remains active, so unrelated turn updates cannot be
counted together. Worker HTTP inherits its process sink without losing request
cancellation or trace context. Errors also capture goroutine count and Go memory
figures. Database/peer positions, main gateway and persisted session snapshots
still depend on the session/mesh phases; they are not invented here.

A real subprocess adapter exiting with code 9 exposed two old stdin shutdown
problems: exec.Cmd.Wait could wait on a copied stdin reader after process exit,
and SDK cancellation could hold the rewriter mutex while cleanup tried to close
it. Both ACP process paths now use the standard Cmd.StdinPipe and close that pipe
before acquiring the rewriter mutex. The subprocess regression verifies the
exit code and stderr event, deliberate shutdown suppression and prompt failure;
three repeated race runs finish in 1.125 s instead of waiting for a ten-second
request deadline. Failure paths close their pipes and event logs.

make ci passes. Mac obs/CLI/network/integration race suites pass; a final Mac
network race run covers the stop and trace refinements. Regressions cover real
worker start/restart/exit spools, ACP start/restart/stop, deduplicated restart
requests, per-request trace inheritance, cancellation and overlapping prompts,
bounded stderr including a 1 MiB write, credentials, private response metadata,
and turn counters after replay eviction. The native child fixture still checks
its terminal descriptors and size and now verifies its inherited trace after
restart. No installed Mac profile or state was changed.

Five isolated Linux amd64 setup runs give medians of 0.301083 ms for
MaterializationHome, 7.403948 ms cold Codex setup and 0.114826 ms warm (previous:
0.302059/7.509646/0.117226 ms). The gateway-disabled cold/warm launch guard still
passes without logs or state.db. These are not Linux ARM64 reference-host results.

## Browser telemetry — P1A.7

The shared API client generates W3C traceparent headers and records API
breadcrumbs. A bounded 30-entry buffer also records route patterns and named
controls. Form values and conversation content are excluded; credential and
prompt-like search fields are omitted. API failures use status/path summaries,
never response bodies. ApiError instances are excluded at subsequent error
boundaries, and already-reported network errors are tracked weakly, preventing
response text or duplicate network reports from escaping through the router.
Window errors, unhandled rejections and router catches use the same reporter.

Repeats within ten seconds are dropped, with at most twenty events per minute.
Batches contain at most five events and split further when their actual UTF-8
body would exceed 64 KiB. Regular uploads use fetch; pagehide uses sendBeacon
with a keepalive fetch fallback. Failed uploads do not report themselves.

POST /api/telemetry requires an authenticated browser cookie, including when a
caller has a valid local bearer. It limits the body to 64 KiB and each session
to sixty events per minute. Only declared error fields and breadcrumb keys are
accepted. The server owns user/node/time/build metadata and applies the existing
event redaction and size limits. Session-rate bookkeeping is pruned with expired
or removed browser sessions. Browser errors use the existing UI fingerprint and
issue machinery. UI build IDs and hidden source maps remain the next step.

Tests cover ring eviction, dedupe, minute limits, status exclusions (400, 401,
404 and other client errors), failed response streams, body exclusion even at
the router boundary, controls versus form/chat text, multibyte batch splitting,
beacon selection and non-recursive upload failures. Go tests exercise real day
files, identity/time spoofing, redaction, rejected local bearer/anonymous calls,
oversized/trailing bodies, independent session limits and minute rollover.

A subprocess Go fixture gives the actual React UI an authenticated gateway and
SQLite day store, with a deliberately malformed session list causing a real
component render error. On leaving the page, the store contains exactly one
ui.error with breadcrumbs and the last API trace. API fixtures live in the test
server, avoiding a race in browser request interception during unload. Final
Chromium and physical Apple M1 WebKit runs take 509 ms and 789 ms respectively.
make ui-e2e prepares the pinned static library and Go test binary; the direct
frontend-only runner skips this integration case unless that binary is supplied.

make ci, lint, typecheck, all 186 Vitest tests (2.88 s), the UI build and size
gates pass. All 189 Chromium/phone browser tests pass in 32.8 s with four workers.
An existing older-peer assertion expected superseded banner text; it now checks
the current machine-details update notice. Mac network race tests and the final
telemetry race regression pass. The embedded Go artifact passes the UI smoke;
that script now uses Make's actual bin/ap output and a task-specific temp variable.

The standalone minified client is 1,356 bytes gzip, enforced by the build's size
gate. The complete initially loaded JavaScript grows from 160,045 to 161,228 bytes
gzip (+1,183), below both the 1.5 KB addition and 170 KB initial budgets. No runtime
dependency or lockfile change was needed. Mac validation uses the temporary
checkout and developer-tool caches, without changing installed application state.

## Hidden source maps and UI build identity — P1A.8

Vite now emits hidden source maps. Staging filters *.map files from the embedded
tree, and the asset handler also refuses .map and .map.gz requests if a map was
accidentally present. The archive remains separate from the executable: a native
GoReleaser files-only archive packages ui/dist/assets/*.map as
ui-sourcemaps_<version>.tar.gz. No custom archive script or runtime dependency was
added. See [GoReleaser's archive configuration](https://goreleaser.com/customization/package/archives/).

GET /api/config exposes only ui_build_id, extracted from Vite's entry filename.
It is public and no-store, including through the conditional-response middleware.
The root loader reads it through Query. Every browser error includes its loaded
entry hash, with the configuration as a fallback; an already-open tab keeps its
own script's identity when the server deploys a newer UI. Bootstrap errors can
use the script hash before configuration arrives. The existing browser regression
now verifies the persisted ID against the gateway configuration. The standalone
telemetry client remains under budget at 1,372 bytes gzip.

Controlled Linux amd64 builds of the same Go/UI source with maps disabled versus
hidden produce executables of 49,639,720 and 49,639,728 bytes: +8 bytes. Vite's
chunk names change between modes, but zero map files enter the embedded tree.
The built gateway passes the artifact smoke, including no-store configuration,
entry-hash consistency and 404s for both map URL forms.

Native make release-check on the Apple M1 finished in 2m56s, building all five
targets and producing ui-sourcemaps_0.0.0-SNAPSHOT-none.tar.gz (1,616,094 bytes,
70 maps). Runtime artifact validation checks its checksum entry, map-only members,
version-3 JSON and the entry map's source content/mappings. Static validation
requires the archive declaration, hidden maps and both local/workflow artifact
checks. Make's release-check regex now escapes its dollar for Make expansion.

make ci, lint, Vitest and build/size checks passed. The native snapshot had already
finished when the user requested fast checks only. Subsequent checks were focused:
the build-ID/cache/map response cases take 6 ms and the browser case takes 257 ms.
No further broad or slow test run was started after that preference. Linux ARM64
reference-host startup measurements remain outstanding.

## Offline diagnostics commands — P1A.9

The logs command now lists, shows, filters and traces retained diagnostics, changes
issue status, and exports a reproduction folder. Reads use plain SQLite read-only
connections and the bounded spool parser; an empty home stays absent. Canonical
counts and their publication cursors share one read transaction, and unpublished
day counts and unconsumed spool lines are merged without replay inflation. Query
accepts only typed flags and literal grep, with bound SQL parameters. Trace and
bundle exports include complete retained traces, including successful parent spans.

Resolving requires a fix version. Delayed occurrences predating that resolution
still contribute to counts but cannot reopen it, including when the gateway later
publishes previously pending counters. A subsequent occurrence at or above the fix
version reopens it. Ignored issues remain ignored.

Bundle exports contain issue.json, traces.ndjson, rows.json and versions.json, in
a private new folder. They reuse the support capsule's private-path policy and
the recorder's credential policy. URLs, absolute paths, user identities and private
row names are removed, while public UI asset names and build IDs remain available
for symbolication. Existing destinations are refused. Related session and mesh-node
rows are selected by involved IDs only; their production tables arrive in P2/P3,
so current exports explicitly report those unavailable tables instead of inventing
rows. Fixtures exercise actual row export against the intended table names.

Focused Linux checks pass: offline readers 0.15 s, all eight CLI commands in text
and JSON 0.26 s; the affected spool/regression/capsule checks also pass. The same
focused checks pass on the physical Apple M1: obs 1.04 s, CLI 0.63 s, capsule 0.30 s.
No broad suites, snapshots or slow tests were run for this step. The Linux ARM64
reference startup measurements are still outstanding.

## Diagnostics tools and authenticated routes — P1A.10 / P1A.11 backend

The gateway MCP server exposes list_issues, get_issue, get_trace and resolve_issue.
All four call the same authenticated REST routes as the browser; the first three
are marked read-only. Inputs use the SDK's typed schemas, errors use its tool-error
response, and text is explicitly described as untrusted diagnostic content. MCP
loopback calls now propagate their parent trace through the shared API caller.

The REST routes provide issue listing/detail, complete retained traces, and
resolve/reopen/ignore actions. They retain the gateway's login, origin and local
bearer checks. Resolution identity comes from the authenticated actor, never the
body. Errors are recorded through the request event. Focused tests cover all four
tools, their annotations, persisted resolution, invalid arguments, unknown issues,
login and origin rejection; the existing MCP inventory/policy regression also
passes. The Linux run takes 0.21 s. No broad or slow suites were run.

## Diagnostics Settings pane — P1A.11

Settings includes a lazy Diagnostics pane with status filters, issue details,
machine counts and versions, sample JSON and recent trace IDs. Resolution stays
inline, requires a fix version and accepts an optional note; success updates the
detail/cache, invalidates lists and shows the existing Hud. Reopen and Ignore use
the same mutation path. Loading, retry, empty and form-error states use the kit.
The single column and wrapping identifiers remain readable at 390 px. No design
tokens, dependencies or system documents were changed.

Types, focused lint, the two model tests and the browser flow pass; the UI build
passes every size gate. The lazy pane is 3,023 bytes gzip. Desktop light and phone
dark captures were inspected against existing Settings; computed input colors and
horizontal overflow were checked after the theme transition settled. The required
fresh Impeccable finish reviewer returned ship with no material fixes. Context and
detector tooling was unavailable (the supplied executable has no execute permission),
so review used PRODUCT.md, DESIGN.md, SURFACE.md, code and the rendered captures.
The physical Mac mini passes the same browser behavior in WebKit. Capture work is
opt-in so ordinary behavior runs avoid screenshot and theme-transition costs.

The final focused behavior run takes 1.1 s in Chromium and 2.0 s on the physical
Apple M1 WebKit. The hour-long normal-use measurement requested by P1A.12 has not
been run under the user's later fast-checks-only instruction; its events/hour,
bytes/event and 50 MiB/day projection remain an explicit validation gap. The guide,
data-layout entries and failure-recording/launch rules are documented. This does
not waive the measurement or the outstanding Linux ARM64 startup certification.

## Offline spool storage bound — P1A hardening

Spool writers now enforce the 16 MiB directory budget before appending, even while
the gateway is stopped. The existing gofrs/flock dependency serializes reserve,
oldest-first pruning and append across processes; gateway pruning uses the same
lock and helper. Nothing is opened until the first event. A sparse-file regression
starts two writers against an almost-full offline spool, verifies oldest eviction
and the total byte bound, and confirms no state.db was opened. The affected spool,
offline-reader and CLI checks pass (Linux obs 0.41 s, CLI 0.28 s; Mac obs 0.57 s).
Asset staging initially overlapped one Go compile; rerunning the affected CLI check
after staging completed passes. No full suites or snapshots were run.

## Account schema and primitives — P2.1 / P2.2

Migration 0006 adds users and local hashed-token auth_sessions, with email and
expiry indexes. Account primitives normalize mailboxes through net/mail, lowercase
them, enforce the 254-byte address and 12–72-byte password bounds, and use bcrypt.
The initial owner claim is one conditional INSERT; 20 simultaneous attempts yield
exactly one owner. Member creation requires an existing claimed gateway. User
listings never expose hashes. Unknown, disabled and incorrect logins return one
generic error; unknown/malformed accounts still execute a default-cost dummy
comparison.

Password changes and disabling are transactional with session revocation. The
revocation stamp advances past existing tokens even within one clock tick, and
local tokens are removed in the same transaction. The primitives needed by the
offline CLI have one exported definition in the network subsystem. HTTP, cookie,
legacy-claim and frontend wiring are the next steps; the old root login is still
in place at this intermediate checkpoint.

Focused migration/default/reopen and account lifecycle/concurrent-claim checks pass:
Linux state 0.10 s, network 0.64 s; physical Apple M1 state 0.59 s, network 1.24 s.
No broad or slow test suite was run.

## Persistent accounts, ownership and offline administration — P2.3–P2.8

The gateway now claims an email owner instead of issuing a bootstrap secret.
Legacy claims require the current password, retain its exact bcrypt hash, and
remove legacy credential files. Status reports claimed/legacy/authenticated/email.
Only health/status, claim and static UI are available to unclaimed browsers;
the private local node capability retains operator access. Browser sessions store
only SHA-256 token hashes in state.db, survive restart, and slide for thirty days.
HTTPS cookies use the __Host- name with Secure, HttpOnly and SameSite=Strict.

Authentication checks expiry, disabled state and the account revocation stamp on
every use, including open-stream rechecks. Password verification and token minting
are bound to the same account epoch, preventing a password-change race from
restoring a revoked login. Email/address exponential failure backoff starts after
five failures (one second, doubling to fifteen minutes), alongside the existing
global ten-per-minute gate; fake-clock tests need no sleeps.

Browser ownership now compares account email instead of granting access by host
name alone. OAuth consent/code/access/refresh records retain the account and epoch
in migration 0007; disabling or globally signing out revokes every stage. MCP
dispatch runs through the existing REST handler in-process so the authenticated
actor survives ownership checks. Its response buffer retains the prior 4 MiB cap.
New tests verify cross-account ACP denial, MCP attribution, OAuth revocation and
the byte bound. The old unavailable-Tailscale identity test now uses an immediate
local API fixture instead of spending three seconds on a missing daemon socket.

The offline CLI provides user add/list/disable, passwd and confirmed reset-owner.
Password entry uses x/term hidden input twice; actual PTY tests cover add, change
and mismatch. Reset removes browser and OAuth credentials as well as account rows
so reclaiming the same email cannot revive old grants. Gateway error diagnostics
omit arguments, including mistakenly supplied positional passwords. Start/status
and the hub show the unclaimed URL; the hub's optional status read is bounded to
100 ms and does not open state.db.

The final focused Linux matrix passes: state 0.20 s, network 1.64 s, CLI 0.71 s,
hub 0.01 s. The physical Apple M1 matrix passes: state 0.34 s, network 1.06 s,
CLI 0.76 s, hub 0.33 s. Stream logout and the remaining affected OAuth regressions
also pass in 1.24 s. The later rejected-password privacy regression passes in
0.31 s. Full suites and release snapshots were not rerun.

## Account browser flow and documentation — P2.9 / P2.10

Login uses email/password. Fresh claims include confirmation, and legacy claims
also prove the current password while explaining that it is retained. Validation
is inline and byte-based. Account settings show the email, ordinary sign-out and
confirmed sign-out-everywhere, without account-management/password-change forms.
The root loader checks status before requesting configuration, which an unclaimed
gateway deliberately refuses. Fixtures now enforce that refusal. Shared API
fixtures cover shell/configuration reads so focused account tests never fall
through to a live development backend.

Types, focused lint, eleven model/return-path tests and the build/size gates pass.
The Chromium claim/sign-out/login flow takes 0.83 s (0.85 s with the strict config
fixture), global sign-out confirmation 0.50 s and the legacy rejection 0.29 s.
The same WebKit cases on the physical Mac mini pass in 2.0 s, 1.5 s and 0.63 s.
Desktop light and phone dark captures of both claim and Account were inspected;
the fresh Impeccable finish reviewer returned ship with no material fixes. No
design-system tokens, runtime dependencies or lockfile entries changed.

The gateway guide, CLI reference, UI contract, AGENTS.md and obsolete proposal/demo
instructions now describe email accounts and the initial private claim. Searches
find no obsolete public bootstrap commands or status fields. The filename of the
legacy bootstrap credential remains only in explicit removal code and its fixture:
P2.3 requires deleting that existing file, so P2.10's literal zero-match filename
grep cannot also be satisfied without obscuring the compatibility behavior.

The embedded Go gateway passes the artifact smoke with the new unclaimed boundary.
Its config check now verifies refusal before using the private local capability.
Deployment health probes check the new status field. Inspection also found an old
CGO-disabled deployment build path; source deployment now fetches the same pinned
archive and uses Zig/musl with the release's static-link/unwind settings. Fast
command-construction and failure-propagation checks pass in 4 ms. A new cross-build
or remote deployment was not run; prebuilt deployment still needs no local build
toolchain, while source deployment now documents the static toolchain prerequisites.

## Sessions phase: storage review and schema — P3.1

The required worker, presence, client, names, recents, ownership, ACP startup and
EventLog files were read in full before this phase. Worker endpoint addresses and
secrets remain private process files. Session metadata, labels, recents, ownership
and conversation entries move into state.db; the existing public SessionInfo and
conversation response shapes remain the contracts for their callers and fixtures.
Native-stdio presence must register through the local gateway rather than opening
the store in foreground launch setup.

EventLog increments sequence numbers for every append. It rejects single entries
larger than the memory budget, and evicts oldest entries when the budget fills.
Before eviction, adjacent agent-message text chunks can merge: the replacement
keeps the first sequence number and combines both texts. Persistence must retain
ordinary evictions, update the merged entry and remove only absorbed chunk rows;
otherwise a later replay either loses history or duplicates text. The append
notification still receives the original entry, not the retained merged version.

Migration 0008 adds one column per SessionInfo field, local node/owner/end/hidden
metadata, and conversation_events with a composite session/sequence key. Nested
state, capabilities and actor values use JSON text; time values use integer
timestamps. Indexes cover node listings, recent launches and native-session owner
lookups. This step does not change worker behavior yet.

Six focused migration regressions pass on Linux in 0.38 s and on the physical
Apple M1 Mac mini in 0.62 s. They cover schema defaults and event identity, account
preservation when upgrading migration 0007, repeat opens, rollback, concurrent
opens and refusal of newer schemas. No full suite, snapshot or soak test was run.

## Session metadata and recent folders — P3.2 / P3.3

Workers own one state.Store for their lifetime. Registration writes the complete
SessionInfo row; conversation-state changes update that row, and teardown records
ended_at and the final terminal state after stopping the child. Session timestamps
use Unix nanoseconds to preserve the existing time.Time precision. Random session
IDs now use crypto/rand.Text so future mesh replicas cannot collide on the old
small temporary-directory suffix space.

New worker.json files contain only ID, PID, loopback address and secret. Gateway
operations join that endpoint with the canonical SQL metadata, including current
native ACP session IDs after restart. Missing metadata refuses writes. Legacy
running workers can still supply their old embedded metadata until stopped-file
import is implemented. Terminal attachment and readiness checks keep reading only
the private endpoint; they do not open state.db.

Native-stdio presence registers and ends through a private local-gateway endpoint.
Browser cookies and peer routes cannot use it. It records the local actor and
does not make the foreground launcher open SQLite. Repeated cleanup is harmless.
Gateway storage failures record diagnostic context; a failed foreground end
notification writes only an error spool event with the actual binary version.
The successful path creates no diagnostic files.

Session listings query this node's active rows before probing live terminal state.
Recent folders query visible rows, newest per directory/harness/mode, capped at
the existing hundred entries. Forget hides every matching row; a later launch is
visible through its new row. Existing availability and folder-identity checks
remain dynamic. Names persist through restarts and can be changed after exit;
stopped ACP sessions retain their account-based write checks. Recents no longer
write JSON, lock or temporary files. Callers keep the same response shapes; CLI,
hub, project/library and ownership fixtures now seed and assert SQL state.

The focused Linux matrix passes: network 1.47 s, CLI 0.42 s, hub 0.20 s, launch
fast-path regression 0.019 s. The physical Apple M1 matrix passes: network 1.20 s,
CLI 1.03 s, hub 0.58 s, launch fast-path regression 1.30 s. Later endpoint/privacy
checks pass in 0.67 s (network) and 0.54 s (CLI) on the Mac; the separate existing
stop-other-accounts regression passes there in 0.52 s. These are focused tests,
not full suites or startup benchmark medians. No slow suite or soak was run.
The final presence/version checks pass on Linux in 0.28 s (network) and 0.11 s
(CLI), and on the Mac in 0.93 s and 0.86 s; the last Linux MCP/peer ownership
selection passes in 0.33 s. Full commit-hook gates were deferred for fast-only work.

Chat-event storage, native ACP ownership bindings, legacy import and P3.8's
concurrency/benchmark gates remain outstanding. In particular, legacy recent-file
data is not yet imported into SQL; this intermediate branch must not be deployed
as a completed session migration.

## Durable chat and stopped-worker reads — P3.4 / P3.5

EventLog now accepts a caller-owned sink. Flushes contain added/replaced entries,
absorbed chunk IDs and the sequence high-water mark. Ordinary memory eviction
never deletes database rows. Only chunks absorbed by a merged entry are removed,
in the same transaction that stores the replacement. Pending changes flush every
50 ms, earlier under byte pressure, and at close. Slow writes apply backpressure;
failed batches remain available for retry and Close returns the first error.

Migration 0009 brings last_seq and last_event_at forward from P4A.1/P4A.2 because
resume already needs the sequence counter. The counter may exceed the largest
stored key after coalescing; storing only max(seq) would reuse old sequence IDs.
The conversation_truncated flag also survives restart, including oversized-frame
warnings with no retained entry. These fields default to zero for existing rows.
last_event_at follows the session table's Unix-nanosecond timestamp convention.

Workers register their row before creating the SQL sink. Each flush updates
progress and upserts whole LogEntry JSON in one transaction. Identical bodies are
not rewritten. Storage failure records an error without chat contents and cancels
the worker. Resume reads a consistent, byte-bounded tail and restores sequence IDs.
Native session/load replay is suppressed when history is already restored; the
installed ACP SDK v0.13.5 supplies the response-scoped notification barrier.

A stop-during-permission regression exposed two lost final events: the live log
held eighteen while the saved log held sixteen. Cleanup now waits for the closed
SDK reader's queued session updates and active prompt/answer writes before the
final flush. The existing notification gate supplies the shutdown counter; SDK
Done alone precedes notification drain. Five repeated stop/exit checks pass.

The gateway reads saved history when the endpoint is missing or unreachable,
preserving NDJSON, since filters, truncation headers and finite SSE replay.
Snapshots count UTF-8 bytes and contain only complete records. The proxy forwards
SSE Accept negotiation and request tracing. Diagnostic bundle fixtures use the
real session schema, and exported owner_email receives the existing identity
redaction policy.

Focused checks cover fixture reducer equivalence, merged-row replacement, resume
after an old exit, one thousand native replay notifications, rollback of rejected
batches, failure cancellation/privacy, stopped/live history equality, UTF-8 byte
bounds, and two independent writers producing 10,000 rows beside a session reader.
The Linux matrix passes: ACP 0.28 s, network 1.05 s, state 0.29 s, CLI 0.24 s,
launch regression 0.016 s. The physical Mac matrix passes: ACP 1.00 s, network
0.83 s, state 0.32 s, CLI 0.58 s, launch regression 0.95 s. Full suites, race runs,
release snapshots and commit-hook gates were not run during this fast-only work.

The short five-repeat startup benchmarks are recorded in startup-performance.md.
The P3.8 concurrency check passes; the physical Linux ARM64 performance gate
remains unverified. P3.6 native ownership bindings and P3.7 legacy import remain
unfinished, so this is still an intermediate migration checkpoint.

## Native ownership and legacy import — P3.6 / P3.7

Native ACP ownership now reads and writes sessions.started_by and owner_email.
An immutable, hidden session row retains each native ownership scope, keyed by
this database's site ID and the old scope hash. This preserves orphaned legacy
owner records, whose filenames contain a hash rather than the native session ID,
and keeps ownership independent of any particular worker's lifetime. These rows
have no directory or native session ID and are excluded from public sessions and
recents. They stay in replicated metadata when chat events are archived later.
The initial worker row leaves the native ID empty until the ownership claim and
actual row binding commit together, preventing two actors from claiming one
native conversation through concurrent launches. Harness aliases share a scope.

Gateway startup imports stopped legacy workers, names, conversation NDJSON,
native owner files and recent folders. Imports use bounded regular-file reads,
check file identity and metadata before commit, preserve canonical SQL rows with
INSERT OR IGNORE, and remove imported files only after commit. Images and other
support files remain. Folder identities are preserved rather than granting
fresh authorization to a replaced directory. Repeat import is harmless.

Running worker PIDs are skipped conservatively; the live IPC endpoint is also
used when an old record has no PID. Their owner records become readable through
SQL while their source files remain for the old worker. Unreadable files remain
in place and record a structured diagnostic without preventing gateway startup.
Unreadable owner records create a hidden refusal marker until a later successful
import; an unreadable owner directory temporarily refuses native resumes rather
than treating missing ownership as unclaimed.

The focused Linux import/startup/claim selection passes in 0.56 s, the existing
owner/resume/fork/worker selection in 0.35 s, and metadata/name/import checks in
0.56 s. The physical Apple M1 Mac import and authorization selection passes in
1.75 s. These checks include a two-store ownership race, repeat import, orphaned
owners, unreadable sources, live-worker retention and actual gateway startup with
an unreadable chat file. No full suite, Windows runtime check, race build, soak or
full commit-hook gates were run. The physical Linux ARM64 P3.8 performance gate
remains unverified. Mesh synchronization and archiving are not implemented yet.


## Replication change-set validation — P4.2 in progress

Added a deterministic 300-operation, three-database convergence test. A and C
exchange changes only through B; pages cross a JSON encode/decode boundary and
are applied twice to verify idempotency. SQLite value tests preserve NULL,
64-bit integers, reals, text, empty blobs and binary blobs.

The test exposed a pooled reader reporting crsql_db_version below the persisted
change high-water mark. Version reads now include the maximum persisted change
version, preventing a returned page from exceeding its own cursor. The complete
internal/state suite passes locally (1.995 s). internal/obs also passes (1.679 s).
The broader network suite fails compression handshake, gateway close, MCP restart
and restart idempotency checks; those failures remain unresolved. Mesh endpoints,
pull loops, archive behavior and release/deployment verification remain pending.

## Peer change endpoint and migration regression sweep — P4.3

Added GET /api/sync/changes behind the same verified-context and enrolled-peer
check as Library transfers. Schema skew returns JSON with both versions and HTTP
409; invalid cursors and excluded site IDs are rejected. Responses reserve room
for their envelope within the 4 MiB limit. Errors attach to the request's wide
event without logging change contents.

Tests transfer six MiB of conversation rows over multiple bounded pages into a
second database. Other checks cover forged identity headers, missing enrollment,
schema mismatch, transaction boundaries, excluded-only cursor progress, every
SQLite storage type through actual CRR transfer, and atomic data/cursor rollback
when the last row of a page is malformed.

Fixed canceled gateway startup querying node identity with an already-canceled
context. Updated network, CLI and hub fixtures for canonical SQL session rows,
per-database local identity, the real mesh_nodes schema and account-status reads.
The earlier four network failures are resolved. A full `go test ./...` run now
passes, including integration tests; network takes 13.430 s, CLI 2.639 s and hub
0.154 s (the latter two reused in the final full run). `git diff --check` passes.

This remains an intermediate, unreleased change. P4.4 pull scheduling, P4.5 acting
identity, P4.6 owner-only explicit mesh enrollment (including replacing automatic
Library enrollment before expanding its trust to account/history replication),
local all-node reads, UI, archiving and remaining proposal phases are unfinished.
No deployment, release build or startup benchmark gate was performed here.

## Pull workers, explicit trust and account forwarding — P4.4–P4.6

Gateway startup now registers its mesh_nodes metadata and manages independent
pull workers for explicitly approved peers. Each worker performs a cheap site-ID
handshake, resumes its saved cursor, validates bounded pages, and atomically
commits changes with the cursor. Failed connections back off; HTTP 404/409 become
Needs update without terminating the worker. State events wake pulls, with a
30-second timer fallback. The manager holds an internal event subscription so
peer relays stay active without browser viewers, cancels removed peers and joins
all worker goroutines at shutdown. Enrollment mutations wake reconciliation.

Old Library peers do not automatically gain account/history access. Explicit
EnrollPeer records a local mesh approval; legacy and automatically enrolled
roster entries have none. Automatic network enrollment was removed. Owner account
checks protect enrollment/removal, while the authenticated machine-local transport
retains administrative access. Both machines must explicitly approve each other.
The UI still needs the corresponding full-trust explanation and legacy-peer
upgrade action before this is ready to ship.

Proxied browser/MCP calls replace any caller-supplied X-Agentpack-User with the
verified acting email. The receiving peer honors it only for explicit mesh
members and a currently enabled account. Persisted account attribution allows
same-account writes across nodes while preserving stable-node ownership checks
for legacy peer actors. Different, disabled and unknown accounts are refused.

Focused network tests pass in 1.470 seconds before the final small scheduling
checks. They exercise A→B→C convergence, partition healing, Needs update recovery,
periodic refresh without an event, event wakeups without a browser, manager
shutdown, legacy-peer refusal, owner-only mutation and forwarded-account trust.
The diagnostics replication test passes in 0.154 seconds: real errors from two
machines yield two combined counts, their sample survives sync, and resolution
on the second machine reaches the first. The final focused network rerun also
passes; no broad suite, race build or release gate was run after these edits,
following the request for focused fast tests.

Remaining proposal work includes all-node local queries and offline reads/UI,
mesh onboarding for an unclaimed node, local CLI account bindings, archive and
blob/cache phases, wider verification, release and deployment. This checkpoint
is not a completed or deployed implementation of the full proposal.

## Replicated local reads and offline history — P4.7 backend

The gateway sessions and recent-folder endpoints query replicated rows across
nodes. Native session IDs and limit snapshots can also be read locally for remote
nodes. Results include node_id, reachable and the last successful sync time;
session results include ended_at and archived_at. The canonical session scanner
now reads this metadata, while write inputs continue to assign the local node in
SQL. Local CLI session discovery remains local-only so environment restarts cannot
affect another machine. Recent local folders retain filesystem identity checks.

The router infers a session's owning node even without a node query parameter.
Offline conversation reads use SQL in NDJSON or finite SSE form. Live history
requests fall back to the replicated copy if the owner disconnects or returns a
server error. Other offline session actions return 503. Renaming is a replicated
metadata edit and works without the owning machine. Existing legacy non-mesh
nodes retain the prior explicit live proxy behavior.

Limits reads return SQL snapshots immediately and refresh local probes in the
background; remote filtered reads do not run local probes. Newly started sessions
and local native-session probes receive server-controlled node/reachability
metadata. Read failures attach to the HTTP diagnostic event and return a generic
500 instead of exposing storage errors.

A focused end-to-end test synchronizes a gateway, closes the source HTTP server,
and verifies sessions, recents, native IDs, limits and history remain readable;
prompt/restart/stop/fork fail with 503 and a rename persists. It also checks cached
history fallback for HTTP 503 and connection failure. Liveness tests preserve the
last successful sync time after a failed contact. The focused network selection
passes in 0.633 seconds; a broader related session/conversation/import/sync
selection passes in 3.116 seconds. Related CLI and hub checks pass in 0.273 and
0.228 seconds. The final mesh/limits rerun passes. No broad suite or release gate
was run, per the request for fast focused checks.

The frontend still needs to replace its per-machine session/limits fetches with
the unified local response and display offline read-only state. This intermediate
backend checkpoint is not released or deployed, and the remaining proposal phases
remain outstanding.

## Unified browser reads and offline chat — P4.8 / P4.9

Sessions and limits now share one local query across machines. The browser groups
replicated rows by node ID rather than issuing per-machine reads, and session
routes load the saved metadata even when their owner is offline. The redundant
browser limits snapshot cache was removed; SQL supplies offline persistence and
the ordinary query cache preserves already rendered data during navigation.
The default machine inventory is local too; replication settings explicitly
request live discovery only when finding machines to enroll.

Offline chats show an Offline, read-only banner with last successful sync time,
retain the thread, hide the composer, disable permission/elicitation inputs and
shortcuts, and prevent terminal switching and forks. Copying remains available.
Offline renames remain enabled because they edit replicated metadata. A stale
live configuration error cannot hide otherwise readable offline history.

Replication settings explains full trust, confirms that password hashes and chat
history will be shared, lets existing Library peers explicitly enable mesh sync,
and shows last sync, schema, lag and errors including Needs update. Sync now wakes
the state worker as well as exchanging Library objects; its HUD says requested,
not complete before the asynchronous pull finishes. Contact updates invalidate
the replication pane without generating extra SQL writes or empty-pull logs.

Added concepts/state.md, linked it in SUMMARY.md, updated ACP and UI guides and
recorded the explicit trust/offline UI invariants in AGENTS.md. Phase 4 is still
not a full-proposal completion claim: unclaimed-node mesh bootstrap and local CLI
account binding, archive, package/blob migrations and release gates remain.

Focused evidence: 21 frontend unit tests pass in 1.25 seconds; build/typecheck,
lint and bundle size gates pass (telemetry remains 1372 bytes gzip). Thirteen
related Chromium browser checks pass in 3.8 seconds, covering offline reload,
disabled permission input, explicit trust, limits refresh and session navigation.
The existing replication-row check passes in 1.2 seconds including browser startup.
Focused network checks pass in 0.676 seconds. Desktop and 390px phone captures
were inspected; the thread and offline banner fit, with no composer or horizontal
overflow. These are Chromium/emulated viewport checks, not physical-phone tests.

The default browser-test port was serving an unrelated app. Verification used a
temporary isolated preview on port 4186, leaving that app untouched; the temporary
configuration was removed afterward. Session/settings fixtures were corrected for
the required config read and explicit discovery query. No full test suite, soak,
physical-device run, release or deployment was performed during this fast-check
checkpoint.

## Mesh bootstrap, local account binding and reset generations

Added `gateway join NODE` for a new machine. After the operator types `trust`, it
records a pending join and explicit peer approval before starting a gateway.
The command prints the remaining start/reciprocal-approval steps. Owner claiming
is refused while a join is pending, including inside the claim transaction;
concurrent preparation and claiming cannot both win. The joining screen polls
for inherited accounts and transitions to ordinary sign-in without offering a
second owner form. Before accounts arrive, verified peers can use only approved
sync/event routes; other application routes are refused.

Added `gateway whoami EMAIL`. Authenticated local actions map to that enabled
account, or the owner by default. Native presence uses the mapped actor. Legacy
histories owned by this OS user retain local-only access; a bound member cannot
write the owner's account-owned chats. Disabling a binding refuses access rather
than falling back to owner. Both join state and account binding are local columns
in local_node. Migration 0013 adds them without replicating local authority.

A reset/reclaim audit found old peer browser and OAuth logins could revive when
the same email was recreated with the original zero epoch. Migration 0014 adds
mesh_settings (CRR) and a local auth_sessions.session_epoch. Reset advances a
replicated epoch before deleting users; new accounts inherit it. Cookie reads,
sliding expiry and OAuth consent validate the login's exact epoch. The shared
key survives user deletion, so clock skew cannot make old future-dated sessions
valid again. The two-store regression verifies old browser and OAuth rejection
and a successful fresh login after reclaim.

Focused network checks pass in 1.135 seconds, related CLI checks in 0.156 seconds
and replicated schema checks in 0.144 seconds. Join/binding/claim-race selections
also pass in under a second per package. Eleven auth UI unit tests pass in 428 ms;
build/typecheck, lint and bundle size gates pass. The joining browser test passes
in 2.1 seconds including startup; desktop and 390px emulated-phone captures were
inspected. Verification used an isolated temporary preview port, with its config
removed afterward. No full suite, race build, physical-device run, release or
deployment was performed here. Archive and remaining proposal phases still need
implementation and final verification.

The final related browser-session revocation, account lifecycle, unclaimed
lockdown, OAuth revocation and mesh bootstrap selection passes in 1.307 seconds.
`git diff --check` also passes.

## Main gateway, archive and failure-trace forwarding

Added `gateway main`, `main set` and `main archive-ready [--empty]`. A switch
compares both roles against the confirmed snapshot, prints the actual old archive
holder and both paths, and requires `switch`. Repeating `set` cannot finish an
interrupted handover. Archive readiness validates a private plain SQLite file;
invalid files, links and copied state databases are refused without replacement.
Main badges are included in inventory/identity/topology and the Machines UI.

P1A.14 now forwards retained failed traces to Main through explicitly approved
peer-only routes. Day files retain positive error-trace forwarding positions;
late failures promote earlier spans beyond acknowledged positions. Per-main/day
cursors advance only after an upload commits. Batches are bounded and drained;
lost acknowledgements and restarts are idempotent. Ordinary healthy traces stay
local. CLI and MCP trace reads merge Main; all-nodes additionally asks peers in
parallel. Forwarded spans retain their original node identity and redaction.

The hourly archiver has injectable time and interval, rechecks both roles inside
each session transaction, skips incomplete/recent/attachment chats, commits the
archive with FULL durability, verifies row counts, then atomically prunes and
marks shared history. A failed prune keeps the shared source. An unmarked retry
refreshes its earlier copy, so changed/coalesced bodies cannot leave stale data.
Archived NDJSON and SSE reads use Main through any gateway; missing or unreachable
archives return `503 archive unavailable`, with Main named in the UI. Read-only
archive opens never create a missing file. All writes, including rename and native
resume through harness aliases or another directory, refuse archived sessions.

A two-replica race test exposed a further issue: a worker can coalesce an existing
event before receiving Main's archive marker, and CRR deletion wins before the
late body becomes visible. Migration 0015 therefore replicates body-free archive
event clocks. Archive pruning remembers original clocks; sync captures newer
archived body edits and coalescing deletions before CRR applies tombstones, commits
the archive first, and acknowledges its state cursor afterward. Keys use the
extension's own unpacking function and comparisons use SQLite values. New tail
rows are safely folded into the archive on read or the next archive pass. The
marker is never cleared. Regression coverage includes late coalescing, stale
gossip, copy/prune crash recovery, row-count mismatch, and another late flush after
copying the archive to a new Main.

Focused final checks pass: state 2.373 s, network 3.390 s, CLI 0.527 s, diagnostics
0.384 s. The complete diagnostics package also passes in 1.478 s. Thirteen UI
tests pass in 1.21 s; build/typecheck, lint and bundle-size gates pass (telemetry
1372 bytes gzip). Earlier Main badge/offline browser checks and inspected desktop
and 390px captures cover the badge. No new browser test was needed for the archive
error banner; its state has a focused Vitest regression. Temporary browser config
and the local SQLite probing test were removed.

This is not a full-proposal completion or release claim. Attachment chats still
stay shared until P5.5; blob pointers, verified peer fetch, non-main attachment
cleanup, cache/Library migration and final release/deployment gates remain. Very
large peer trace replies currently have a 16 MiB transport ceiling; final protocol
verification must address pagination before claiming every-sized cross-node trace.
No full repository suite, race build, new soak, release or deployment was run at
this checkpoint. Launch/staging/fingerprint code was not changed here.


## Relayed archive edits, attachments, and peer content — P4A/P5 follow-through

The archive race protection now uses immutable replicated `archive_edits` rows
(migration 0018), rather than refusing late edits on intermediate machines. Before
CRR can absorb an old event body or coalescing deletion, the receiver publishes a
bounded independent backup version. The original received page and source cursor
still commit atomically. A rejected page may leave a harmless authored backup.
Main drains the journal after a pull, before an archived read and during its
hourly pass; archive commits precede each journal acknowledgment. Stable edit IDs
and replicated acknowledgment tombstones prevent stale replay from recreating
already processed edits. Migration 0017 tracks the finalized archive sequence.

The owner refuses an archive marker while a turn is working or awaiting approval.
Prompt and turn-boundary persistence flush synchronously before idle state is
published. Worker shutdown now removes only its endpoint files, preserving images
and support files. Tests cover an idle late tail, pending handover, a new Main
owning the late edit, three-node delivery after the original worker closes, stale
replay, and failure after archive commit but before the journal acknowledgment.

P5 adds replicated package/blob pointers, gateway-only verified-stamp advertising,
peer-only tar transport and verified peer-first restoration. Standard archive/tar
and os.Root preserve executability and safe in-package symlinks. Missing or corrupt
peer copies fall through to the existing GitHub installer; verified cached launches
never invoke the peer hook. Blob copies verify their SHA before durable publication
under the existing per-session quota, and quota publication is protected by a
cross-process lock. Durable rename now belongs to internal/paths and is shared
with registry publication.

Attachment chats now archive: Main fetches missing referenced images first, then
checks the exact stored prompt references and local hashes under the archive
transaction. Migration 0019 retains replicated attachment references and local
cleanup progress. Non-Main cleanup waits for the finalized sequence, ready roles
and an actual hash-verified response from Main, rather than trusting a stale
location row. A handover or corrupt Main copy preserves source files. Main keeps
its files. Archive readiness verifies and advertises copied attachments, fetching
missing ones through the running local gateway; backup/switch instructions include
attachment directories.

Focused checks passed: network/state/CLI 3.102/1.494/0.423 s. The archive/worker
race sweep passed (ACP/state/network 1.019/1.131/4.651 s); the final focused race
regressions including attachment preservation and failed journal acknowledgment
passed in 2.341 s. Existing cache/resolver/registry checks and UI checks from the
previous checkpoint remain relevant; no UI source changed in this follow-through.

Peer-transfer startup measurements (median of five runs, 5 iterations each,
32 MiB history fixture, Linux amd64 i9-12900K):

| Benchmark | Before (ms) | After (ms) |
| --- | ---: | ---: |
| MaterializationHome | 0.350419 | 0.353678 |
| CodexSetup/cold | 8.301259 | 8.162245 |
| CodexSetup/warm | 0.128611 | 0.149694 |

The benchmark arms a peer callback that fails immediately if launch setup calls
it. These are local host measurements, not the Linux ARM64 release reference.

## Disposable cache index on SQLite — P7

`cache/index.db` replaces bbolt metadata and aliases, using plain SQLite with the
extension explicitly disabled, private creation permissions, WAL, NORMAL sync and
read-only query connections. The old `cache/db.reddb` is deleted on first open;
package trees and lockfiles are untouched. Cache/GitHub operations retain their
exported names and semantics. Library bootstrap watches both database and WAL
metadata. `go mod tidy` removed go.etcd.io/bbolt and its two checksum entries.

Full focused packages passed: database 0.016 s, cache 0.111 s, GitHub 0.092 s,
resolver 0.013 s. Cached-add/offline resolver checks passed in 0.029 s; the Library
bootstrap regression passed in the CLI sweep above. Tests cover the disposable
legacy index, private plain SQLite without cr-sqlite, persistent aliases, read-only
writes and nonregular database rejection.

Before/after medians of five runs with 10 iterations each on the same amd64 host:

| Benchmark | bbolt (ms) | SQLite (ms) |
| --- | ---: | ---: |
| CachedAdd resolver | 0.051531 | 0.329547 |
| MaterializationHome | 0.315432 | 0.331153 |
| CodexSetup/cold | 6.699471 | 7.064142 |
| CodexSetup/warm | 0.129086 | 0.135908 |

Reproduce the current side with:

```sh
go test ./internal/sync -run '^$' \
  -bench '^(BenchmarkCachedAdd|BenchmarkMaterializationHome|BenchmarkCodexSetup)$' \
  -benchtime=10x -count=5 -benchmem
```

The controlled bbolt baseline used database/cache.go, cache/index.go,
github/metadata.go, paths/paths.go and go.mod/go.sum from
5bbdf394889cbac74df882d758fb9478cc631ddb, with the same current benchmark and
remaining source. SQLite connection setup for the two alias/record lookups adds
about 0.28 ms to cached-add resolution. No connection cache or new abstraction was
introduced for that submillisecond cost. Verified warm launch setup still does no index or peer
I/O. An initial after-run overlapped focused tests and was replaced by the isolated
run reported above.

Remaining: P6 Library migration/removal of the old object-transfer protocol, large
trace reply pagination, live legacy-worker upgrade audit, final full checks,
reference-host performance/soak, product release and deployment. The 4 MiB change
page limit also needs a final oversized-event/transaction audit, including encoded
journal overhead. No release or deployment is claimed by this checkpoint.


## Library storage and one state protocol — P6

The Library now stores immutable public objects, name objects, head metadata,
tombstones and pending drafts in state.db. Private raw credential bindings and
peer approvals are local SQL tables; captures and interrupted-restore journals
remain files so foreground launch setup/recovery never opens state.db. Effective
heads are derived from immutable revisions, preserving clean three-way merges,
concurrent conflicts, explicit resolution, normalization and redaction behavior.
Object validation is memoized by digest and the replicated conflict position;
row tampering invalidates the proof. A test makes temporary snapshot validation
unavailable after the first read and proves an unchanged revision is still reused.

First open verifies every legacy public digest and reference, copies bindings,
drafts and metadata durably, then removes imported files and empty directories.
A corrupt source leaves all sources intact. Conflicting private bindings/drafts
are refused with both copies retained. Legacy peer approvals preserve their mesh
flags and removals; they never replicate. Unknown support files are retained.
Drafts store stable target node IDs internally and expose the local alias only on
the owning machine; binding an offline site identity updates draft targets too.
A replicated draft can never silently retarget the receiver's own folder.

The inventory/object/ack protocol, transfer caches, receipts and its serial polling
loop are removed. Library changes use the same bounded state pages as accounts and
chat history. Public record digests/types/redaction are checked before applying a
page, even when the Library UI is disabled. Each applied Library page runs
MergeHeads and collection; retained tombstones plus CRR deletion clocks keep a
returning offline node from resurrecting deleted records. Settings retains local
peer administration and sync controls, displaying approval/contact state without
obsolete receipt counts. Draft review retains its bounded peer request.

The existing behavior tests were retained, with filesystem-specific setup/assertions
translated to SQL and retired protocol fixtures translated to state pages. A real
two-gateway HTTP test captures on A, reads on B, diverges on both, checks identical
conflict views, resolves, and propagates deletion. A corrupted object is rejected
before either the destination object or source cursor changes.

### Pinned extension transport-clock correction

The two-gateway resolution regression exposed a mismatch that previous narrow
replication checks missed: the pinned superfly fork uses per-author database
versions, unlike the global versions described by upstream documentation. The
previous maximum-of-surviving-clock-rows cursor could move backward after a
foreign winning value was replaced, or skip a low-version change relayed by a
busy node. See the [pinned fork's bookkeeping model](https://github.com/superfly/cr-sqlite/blob/8e32195704ccaea8508f7d378e11dc2dc0bb3d2e/README.md).

Migration 0021 reconciles the persisted local counter with known versions. Incoming
source-version groups receive monotonic local transport versions within the same
transaction as their data and cursor. Author, column version, lifetime and sequence
are preserved. The persisted counter, rather than surviving rows, supplies the
high-water mark. Writer connections retire between transactions so a following
native write reloads the advanced counter; no extension source or pinned asset
changed. Self-authored echoes cannot modify the receiver's own records. Tests
cover three-node low-version relay, the next local edit through the same writer
pool, deletion, rollback and randomized convergence.

### Verification and performance

Final full phase checks passed: registry 8.931 s, network 29.143 s, state 7.389 s,
CLI 5.780 s (packages ran together). Focused race checks passed: registry 2.787 s,
state 1.550 s, network 2.463 s. Additional draft-target and collection regressions
passed in registry/network/state 0.989/0.828/2.077 s. Settings label tests (4), UI
build/typecheck, lint and bundle-size gates pass; telemetry remains 1372 bytes gzip.
No old inventory/ack route or receipt field remains in internal/ or ui/src.

Same-host median of five runs, 10 iterations each, compared with the P7 after-run:

| Benchmark | Before P6 (ms) | After P6 (ms) |
| --- | ---: | ---: |
| CachedAdd resolver | 0.329547 | 0.310703 |
| MaterializationHome | 0.331153 | 0.321437 |
| CodexSetup/cold | 7.064142 | 6.704513 |
| CodexSetup/warm | 0.135908 | 0.131848 |

Use the same benchmark command documented under P7. This is Linux amd64, not the
ARM64 reference host. The existing cold/warm-launch regression checks that state.db
is neither created nor changed. No new launch-path database access was added.

Remaining before release: large trace reply pagination, oversized state-version
and journal transport audit, live legacy-worker upgrade audit, the final proposal
completion audit, full make ci/UI/artifact/release-check gates, reference-host
performance and soak evidence, then product release and deployment. No product
release or deployment has been performed at this checkpoint.


Follow-up draft regressions cover concurrent saves during the first stable-node
binding. Target normalization happens inside the SQL insert/import transaction;
reads recognize prior local aliases. The focused alias/import race check passed
in 1.522 s. Legacy draft fixtures retain their original local-target semantics.
Peer draft review keeps its prior 64 MiB bound while the retired object-transfer
helper is gone. The cold/warm launch state-file regression passed in 0.017 s.


The remaining protocol audit must also reconcile the proposal's named
`GET /api/sync/events` stream with the current reuse of `/api/events` for mesh
wakeups. Functioning wakeups alone do not prove that named endpoint delivered.
The final Library draft peer-bound check passed in 0.806 s.


## Final protocol audit: event authentication, trace pages and oversized versions

`GET /api/sync/events` now exposes the named, explicitly enrolled mesh stream.
Peer clients select it for mesh peers; existing browser and legacy event routes
remain available. Stream revalidation checks peer enrollment rather than browser
cookies and closes a removed peer's stream. Focused event/authentication tests
pass (3.784 s).

Trace reads use stable timestamp/node/span cursors across day files and pending
spools. Peer HTTP replies and MCP `get_trace` return 16-event pages; the CLI
collects every page. MCP accepts `cursor` and reports unreachable requested peers
in `unavailable_nodes`. A 1,600-span trace larger than 16 MiB crosses 100 peer
pages without missing or duplicated spans. A separate regression ingests spools
between pages; authenticated MCP follows three pages and preserves all 35 spans.

Oversized source versions no longer stop replication at HTTP 413. Schema 0022
negotiates a chunked fallback: source-side SQLite snapshots stream one version to
a private temporary file, a random peer-bound token identifies that immutable
snapshot, and chunks stay below the existing 4 MiB HTTP limit. The receiver
checks offsets, source identity, version and a whole-transfer SHA-256 before
applying all changes and its cursor in one transaction. One source snapshot per
approved peer expires after two minutes; normal gateway shutdown removes it.
A corrupted chunk leaves both rows and cursor untouched, retry succeeds, another
peer cannot reuse the token, and source edits cannot change an in-flight copy.
The receiver decodes the complete verified transaction before applying it; its
memory therefore scales with the largest transaction, not just the HTTP chunk.
Temporary files are private and derived; an abrupt process kill can leave an OS
temporary file, which is not treated as durable state or resumed.

Focused state/network checks pass (0.128/1.023 s). Focused race checks covering
oversized versions, event streams and large trace reads pass (1.117/13.076 s).
Authenticated MCP/legacy-import/oversized-transfer checks pass together (0.791 s).

The live-worker audit follows P3.7 exactly: running legacy workers retain their
endpoint, metadata, ownership and history files, and stopped workers are imported
on a later gateway start. No speculative live-worker migration bridge was added.
Existing import regressions verify live-file preservation, unreadable-source
failure reporting, idempotence and preservation of canonical SQL rows. Legacy
workers are not added to the SQL-backed mesh lists until that stopped import.

The final full gate found and removed an unreachable return in Library deletion.
UI tests also exposed a duplicate local-session refetch; local events now refresh
once, peer events refresh unified and peer caches, and state hints refresh unified
state. All five event-provider tests pass (0.785 s). Full gates are still running;
this entry is not a release or deployment claim.


## Final gates and native ARM64 setup measurements

The completed checkout passes `make ci`: formatting, vet, all Go packages,
all race tests and the product build. An unlocked test-only persistence-byte
assertion was fixed before accepting the race result. The final UI unit suite
passes all 198 tests across 37 files; lint, TypeScript, build and every size gate
pass. `make artifact` stages the actual UI, rebuilds the binary and passes its
real-gateway smoke test. Telemetry remains 1,372 bytes gzip.

Browser checks now cover the unified response contract, tagged peer rows, one
sessions/limits read, routing mutations to their owner, explicit trust confirmation
and sync progress. Chromium and phone pass 195 tests in 21.2 s. WebKit runs on
the physical Mac because this Linux environment lacks its system libraries:
11 applicable tests pass in 9.8 s; two existing browser-specific cases are skipped.
The selection test uses xterm's native platform modifier (Option on macOS,
Shift elsewhere). Startup's connecting announcement moved to the root loader,
which owns the first status/configuration request. It no longer waits for the
nested application loader to mount.

The native Mac's final `make release-check` passes: GoReleaser configuration,
five platform builds, installers, archives, checksums, source maps and binary
system-dependency tests. The warm snapshot takes 56 s; this is not a substitute
for the final GitHub release workflow duration or its native platform smoke jobs.
No installers or product release have been published at this checkpoint.

Native Linux ARM64 measurements on oracle-vm (Neoverse-N1, two virtual CPUs)
meet all setup budgets. Median of five repetitions, ten iterations, 32 MiB
history fixture: MaterializationHome 0.663780 ms (<1), CodexSetup/cold
17.508913 ms (<20), CodexSetup/warm 0.350938 ms (<1). The original reference
hardware was unspecified, so this is a deployment-host certification, not a
matched-hardware comparison. Commands are in startup-performance.md.

The hour-long normal-use measurement is running separately from unit tests in
an isolated home with Tailscale sharing disabled, three real terminal workers,
and successful input/read HTTP traffic every 15 s. It uses no model requests
or account quota. Its final storage projection is required before release.

Remaining: final requirement-by-requirement completion ledger, that measurement,
commit/push and required release-workflow checks, publication, and deployment
with live version/health verification. The goal remains active.


Oversized archive transport is covered end to end: a valid late coalesced event
travels worker → intermediate peer → Main over chunked HTTP, becomes a relayed
archive journal entry, and updates the durable archive without changing its body.
The focused transfer/journal group passes in 1.525 s; its race run passes in
10.174 s. Failed snapshot creation closes the temporary file before removing it,
including on Windows; a canceled-transfer regression checks no private file is
left behind. v0.14.0 is prepared in the product source. Implementation commit:
`74996bf` (P4–P7 and transport/diagnostic follow-through).


### Completion ledger before publication

| Requirement | Implementation and evidence | Remaining gate |
| --- | --- | --- |
| P0.1–P0.11 static foundation | Immutable source/toolchain/SQLite pin, five checksum-verified libraries, real extension self-checks, static/system-link validation and version-pinned installers. Earlier native platform workflow evidence and measurements appear under P0. | Current release workflow and final duration. |
| P1.1–P1.7 local store | FULL/WAL state store, migrations, limits, settings and OAuth; local credentials remain local. Successful foreground-launch regressions leave state.db untouched. | None in this phase. |
| P1A.1–P1A.11 diagnostics | Redacted bounded events, daily logs, spools, correlation, exact sampled issue counts, browser telemetry, source maps, offline CLI, authenticated MCP and Diagnostics UI. Full Go/UI gates pass. | None in these steps. |
| P1A.12 volume | Full hour: 1,923 events, 438.78 bytes/event on disk, 19.31 MiB/day projection; three real terminal workers, 720 input requests and 1,200 reads. No model requests. | Pass (<50 MiB/day). |
| P1A.13–P1A.14 shared issues/traces | Replicated issue status/counts, retry-safe failure-only forwarding, bounded paginated peer/MCP reads and complete CLI collection. | None in these steps. |
| P2.1–P2.10 accounts | Claim/login, preserved legacy hash, hashed sliding sessions, role checks, revocation, abuse limits, offline administration, browser/account flow and docs. Joining nodes wait for inherited users. | None in this phase. |
| P3.1–P3.8 sessions | Durable metadata/history/progress, owner-safe native resume, stopped-worker import, coalescing replacements, concurrent writers and DB-free launcher checks. ARM64 setup medians meet budgets. | Live legacy workers intentionally await stopped import, per P3.7. |
| P4.1–P4.9 mesh | CRR tables, matching-schema peer transport, whole-version apply/cursor transactions, explicit mutual trust, event wakeups, independent retry workers, acting-user authorization and unified offline browser reads. Transport-clock, partition, corrupt-transfer and large-version regressions pass. | None in this phase. |
| P4A.1–P4A.8 archive | Single Main/archive roles, safe handoff, seven-day archive commits before prune, late-edit journal, read-only archived chats and verified attachment retention/cleanup. Large late edits relay through a peer to Main. | None in this phase. |
| P5.1–P5.5 content pointers | Verified advertised locations, bounded peer package tar fetch with lock-integrity checks and GitHub fallback, image verification/quota and no cached-launch network requests. | None in this phase. |
| P6.1–P6.5 Library | Verified SQL import, immutable public objects, derived heads/merge/conflicts, stable draft targets, private local bindings/approvals and removal of inventory/object/ack transport. | None in this phase. |
| P7.1–P7.3 disposable index | Plain SQLite index with unchanged callers, safe old-index removal, offline alias reuse, bbolt removed from module graph; before/after numbers recorded above. | None in this phase. |
| Finish | Full make ci, UI checks, native browser checks, embedded-artifact smoke and five-platform Mac snapshot checks pass. v0.14.0 candidate committed as 74996bf and pushed to PR #33. | Required current workflow, volume measurement, final report, merge/release and live deployment verification. |

The required release workflow is running for candidate 74996bf:
https://github.com/olegpustovit/agentpack/actions/runs/43689573922 .
Historical checkpoint gaps above are superseded only by the evidence in this
ledger and the final publication entry; pending items are not counted complete.


The named sync-event endpoint is now checked through the complete gateway,
including a pending join. Its bootstrap allowlist admits only the explicitly
approved peer, and conditional response caching excludes the stream even when
no Accept header is supplied. The same fixture verifies the joining browser can
read its UI configuration while application data remains forbidden. Focused
join/event/cache checks pass (2.438 s); their race check passes (3.495 s).


### Native Windows gate follow-through

Candidate workflow 43689573922 caught three Windows-specific failures. Peer tar
links used Windows separators before POSIX archive validation; the tar filesystem
adapter now validates and converts native link targets as archive/tar reads them,
without changing materialized cache bytes or the sha256-tree-v1 digest. Validation
also occurs at the actual header read rather than in a separate pre-scan. The
existing safe round-trip and unsafe-link tests cover the behavior.

The disposable-index test now checks POSIX permission bits on Unix, since Windows
reports DOS read-only flags instead. Its plain-SQLite, read-only and nonregular-file
checks still run on Windows. The native-presence fixture prepares its schema
before timing server readiness, retaining the same two-second deadline. Focused
cache/database/CLI checks pass in 0.008/0.016/0.114 s. Native Windows must pass the
corrected commit's required workflow before publication; its failed predecessor
is not treated as a passing gate.


### Final report inputs

The source artifact was re-read during the final audit: version 10, index.md
SHA-256 `719d4a7a1f3bd69d53ddeeea703e36ccd04f95f51070a99aec26af3bd8298d28`.
The completion ledger above retains the original numbered scope.

Same-host Linux amd64 setup measurements (32 MiB fixture) compare P0.1 baseline
with the completed implementation's P6 run:

| Benchmark | Baseline (ms) | Final implementation (ms) |
| --- | ---: | ---: |
| MaterializationHome | 0.389806 | 0.321437 |
| CodexSetup/cold | 9.293943 | 6.704513 |
| CodexSetup/warm | 0.139241 | 0.131848 |

The cache-index replacement's separate controlled add benchmark increased from
0.051531 ms on bbolt to 0.310703 ms after SQLite and Library integration. That is
an absolute increase of 0.259172 ms; no numerical add budget was specified. Its
before/after setup measurements and the resolver's armed no-peer-callback check
are recorded under P7 and P6 above. The native ARM64 deployment host independently
meets the explicit 1/20/1 ms setup budgets, as recorded in startup-performance.md.
Earlier store/replication/allocator and diagnostics-overhead measurements remain
in their phase sections; they are not replaced by unrelated setup timings.

No tracked source file was deleted between baseline 0bd1c5b and this candidate.
Removed behavior and data formats: the inventory/object/ack replication protocol,
verified-object receipt bookkeeping, file-backed public Library objects/heads/
names/tombstones/drafts (removed after verified import), and the disposable
cache/db.reddb index (rebuilt as plain SQLite). Local capture settings and recovery
journals remain files to keep launches database-free.

Dependency changes against baseline: remove go.etcd.io/bbolt v1.5.0; add
mattn/go-sqlite3 v1.14.53 for the pinned static SQLite/cr-sqlite store and
felixge/httpsnoop v1.1.0 for HTTP observation while preserving streaming and
WebSocket interfaces. Rust and Zig are build-input tools, not installed runtime
dependencies. Published platform libraries remain immutable and checksum-pinned.


Corrected candidate a230a8b passes required workflow 43691517481: the full native
Windows package/launch/PowerShell suite, GoReleaser snapshot, binary dependencies,
source maps, and all five real-extension platform smoke jobs. The library rebuild
is correctly skipped because all pinned assets already exist; product publication
is correctly skipped for a pull request. This supersedes the failed predecessor
for the native Windows issues above. The storage run and tagged product publication
remain separate gates.


## Completed hour-long storage measurement — P1A.12

2026-11-15 07:09:06–08:09:06 UTC, Linux amd64, isolated private home, Tailscale
sharing disabled. Three real shell terminal workers received input and HTTP
read traffic every 15 s. Workers were stopped cleanly, final spools ingested,
and day databases closed/checkpointed before measuring. This profile uses no
model requests and consumes no model quota.

| Measurement | Result |
| --- | ---: |
| Elapsed | 3,600.030 s |
| Terminal workers | 3 |
| Successful input requests | 720 |
| Successful read requests | 1,200 |
| Retained diagnostic events | 1,923 |
| Retained error events | 0 |
| Cleanly ended workers verified in state.db | 3 |
| Events per hour | 1,922.984 |
| Day database bytes | 843,776 |
| On-disk bytes per event, including tables/indexes | 438.781 |
| Projected MiB per machine per day | 19.312 |
| Required ceiling | 50 MiB/day |

The helper asserts three live workers, successful input/read traffic, observed
terminal output, at least 700 input and 1,000 read requests, nonempty diagnostic
storage, and the 50 MiB/day ceiling. It stops workers and closes the gateway before
computing counts. The measured volume passes; it is a workload projection,
not a claim about every possible user activity level. The 256 MiB daily hard
budget and sampling/retention guarantees remain independently tested.

Corrected-candidate workflow 43691517481 completed successfully in 661 s
(07:47:37–07:58:38 UTC). Baseline product workflow was 577 s; the candidate's
span is +84 s and remains below the Phase 0 697 s comparison ceiling. This
candidate run does not include tagged product publication; the final product
workflow's own elapsed time will be recorded after it completes.


Final current-source Mac `make release-check` also passes after the Windows and
stream follow-through: five platform snapshots, archives/installers/checksums,
and the binary-dependency/source-map integration checks. This snapshot takes
91 s (its predecessor's 56 s was an earlier checkpoint, not a replacement for
this current-source check). Local `make ci` passes again before the final report
commit. Source changes remain unpublished until the tagged workflow succeeds.


## Final publication and deployment — complete

PR [#33](https://github.com/olegpustovit/agentpack/pull/33) merged to `dev`
as `c52f59cd53ba7ffd465929c51bd8a2442093ad01`; annotated tag `v0.14.0`
points to that commit. The tagged
[release workflow](https://github.com/olegpustovit/agentpack/actions/runs/43693895582)
completed successfully. Its native Windows full suite, GoReleaser builds,
five real-extension runtime smoke jobs, binary dependencies, installer/source-map
validation and exact-binary publication comparison all passed. The existing
immutable cr-sqlite inputs were reused; their source rebuild was correctly skipped.

[v0.14.0](https://github.com/olegpustovit/agentpack/releases/tag/v0.14.0)
is published, non-draft, non-prerelease and latest. All nine required assets are
present and nonempty: five platform archives, both installers, checksums and
the separate UI source-map archive. Both install commands at the beginning of
the release body pin v0.14.0. Published installer scripts match the rendered
tested templates byte for byte. Actual published shell installers were exercised
in isolated directories on Linux amd64 and physical macOS arm64; both installed
0.14.0 and passed the real static-extension `state-selfcheck`.

### Release durations

| Run | Workflow timestamp span | Scope |
| --- | ---: | --- |
| v0.13.9 baseline, 42070852362 | 577 s | Product release baseline |
| P0 warmed validation | 551 s | Recorded above; passes the 697 s warm gate |
| Final corrected candidate, 43691517481 | 661 s | Full candidate validation; no tagged publication |
| v0.14.0 tagged release, 43693895582 | 1,320 s | 08:18:40–08:40:40 UTC; complete product publication |
| Final native Mac `make release-check` | 91 s | Current-source five-platform snapshot and integration checks |

The final tagged workflow is 743 s longer than the historical product baseline;
its tag-scoped cache/build and publication span must not be presented as the
661 s candidate run or as an equivalent warmed build measurement. The separate
warm-build gate passed as recorded above. Workflow spans include orchestration
and publication; the local Mac snapshot is a different measurement.

The remaining baseline UI first-frame benchmark was repeated on the same Linux
amd64 host with the original five-by-five command: median **0.017847 ms**, versus
baseline **0.018906 ms** (samples 0.018065, 0.017847, 0.017516, 0.017910,
0.017595 ms). Setup, cache-add, state/replication/allocator and diagnostics
measurements, including their limits and hardware, are recorded in the phase
tables above. The ARM64 deployment host meets all three setup budgets; the
hour-long storage profile projects 19.312 MiB/day against the 50 MiB ceiling.

### Live gateway verification

Before upgrading, the existing gateway was stopped briefly for a private complete
data-home backup. The archive was compared to its source and its SHA-256 checked
before the old service resumed. That backup remains under the remote user's
private `agentpack-gateway-backups/pre-v0.14.0.*` directory. No legacy workers
were running. The house `ap deploy --binary` path installed the published ARM64
binary, retained the previous executable and Caddy configuration, and verified
the local service and public HTTPS endpoint.

At [agentpack.microapps.space](https://agentpack.microapps.space), verification
confirmed:

- Active systemd user service and CLI/API version 0.14.0.
- Installed binary SHA-256
  `b1dddb9277382fa16bc09d262cb4e3c869c19de2907742a96b19f7d0eaf25ea3`,
  identical to the checksum-verified published ARM64 archive's executable.
- Native static-extension self-check `ok`, schema version 22 and state.db
  `PRAGMA quick_check` result `ok`.
- Public page and all loaded JavaScript/CSS assets return 200. A real Chromium
  browser renders the legacy owner-claim form without page errors.
- `/api/status` returns 200 with `claimed=false`, `legacy=true`; unauthenticated
  `/api/config` returns 403. All loaded JavaScript source-map URLs return 404.

The preserved legacy password protects the owner claim. The owner must open the
gateway and confirm that existing password with their email; the upgrade does
not invent an account or replace their password. That account interaction was
not performed with guessed credentials.

### Final gate disposition

P0 through P7, including P1A diagnostics and P4A archive, are implemented and
verified by the numbered ledger and its subsequent evidence. The ledger's
publication, duration, measurement, merge, release and deployment gates are now
closed. P3.7's deliberate stopped-worker import rule remains product behavior;
there were no live legacy workers to defer on the deployment host.

The final code passed `make ci`, all 198 UI tests, lint/typecheck/build/budget
checks, 195 Chromium/phone browser checks, 11 native Mac WebKit checks (two
existing engine skips), `make artifact`, and native Mac `make release-check`.
The full tagged workflow subsequently passed. The final report-only commit
reruns the Go CI gate; no product source differs from the released tag.

No tracked files were deleted. Removed protocols/file formats and the exact
dependency additions/removal are listed under Final report inputs above.
Optional headless model prompts were not run; no account quota or credentials
were required for these checks. This completes the implementation, release and
deployment request; retained backups and user-owned account claiming remain
intact.
