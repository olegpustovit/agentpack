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
- P0.7–P7 and Phase 1A: not implemented.
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
