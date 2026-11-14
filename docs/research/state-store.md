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
- P0.11: build/storage gate audited below; warm CI timing and reference ARM64 measurements remain open. P1.1–P1.4 are implemented; P1.5 onward and Phase 1A are not implemented.
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
| Warm release duration | Baseline 577 s + at most 120 s | First complete CI run took 1,317 s with cold caches; warm rerun 43605315971 is active. Same-Mac warm snapshot is 46–53 s | **Pending comparable warm CI measurement** |
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
