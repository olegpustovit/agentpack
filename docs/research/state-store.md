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
The first read imports valid legacy network/sharing.json with an atomic insert
and removes the file only after a successful write. An existing database value
wins against a racing import. Allow-entry validation also applies to imports;
malformed JSON and failed writes preserve the source for recovery. The local
node key shares the limits P4.1 rekey requirement.

Tests cover defaults, enabled/disabled settings, allow validation, import,
restart persistence, and a forced SQLite write failure retaining the original.
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
