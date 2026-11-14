#!/usr/bin/env bash
# Build the exact source pin; normal builds will fetch verified release assets.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
if [[ $# != 2 ]]; then
  echo "usage: $0 <goos> <goarch>" >&2
  exit 2
fi
goos=$1
goarch=$2
case "$goos/$goarch" in
  linux/amd64) rust_target=x86_64-unknown-linux-musl; zig_target=x86_64-linux-musl ;;
  linux/arm64) rust_target=aarch64-unknown-linux-musl; zig_target=aarch64-linux-musl ;;
  darwin/amd64) rust_target=x86_64-apple-darwin; zig_target=x86_64-macos ;;
  darwin/arm64) rust_target=aarch64-apple-darwin; zig_target=aarch64-macos ;;
  windows/amd64) rust_target=x86_64-pc-windows-gnu; zig_target=x86_64-windows-gnu ;;
  *) echo "unsupported cr-sqlite target: $goos/$goarch" >&2; exit 2 ;;
esac
# tomllib validates the lock instead of interpreting it as shell code.
mapfile -t pin < <(python3 - "$root/third_party/crsqlite.lock" <<'PYLOCK'
import re, sys, tomllib
with open(sys.argv[1], "rb") as f:
    p = tomllib.load(f)
assert p["repo"] == "https://github.com/superfly/cr-sqlite", "unexpected source repository"
assert re.fullmatch(r"[0-9a-f]{40}", p["commit"]), "invalid source commit"
for key in ("repo", "commit", "rust", "zig", "sqlite"):
    value = p[key]
    assert isinstance(value, str) and value and "\n" not in value
    print(value)
PYLOCK
)
[[ ${#pin[@]} == 5 ]] || { echo "invalid third_party/crsqlite.lock" >&2; exit 1; }
repo=${pin[0]}
commit=${pin[1]}
rust=${pin[2]}
zig=${pin[3]}
sqlite=${pin[4]}
[[ $(zig version) == "$zig" ]] || { echo "install Zig $zig from third_party/crsqlite.lock" >&2; exit 1; }
rustup run "$rust" rustc --version
rustup target add --toolchain "$rust" "$rust_target"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
git init -q "$work/source"
git -C "$work/source" remote add origin "$repo"
git -C "$work/source" fetch --depth=1 origin "$commit"
git -C "$work/source" checkout -q --detach FETCH_HEAD
[[ $(git -C "$work/source" rev-parse HEAD) == "$commit" ]] || { echo "source commit mismatch" >&2; exit 1; }
# Upstream records an SSH submodule URL; source builds must also work anonymously.
git -C "$work/source" -c url.https://github.com/.insteadOf=git@github.com: submodule update --init --recursive
actual_sqlite=$(sed -n 's/^#define SQLITE_VERSION *"\([^"]*\)"/\1/p' "$work/source/core/src/sqlite/sqlite3.h")
[[ $actual_sqlite == "$sqlite" ]] || { echo "SQLite version mismatch: source $actual_sqlite, lock $sqlite" >&2; exit 1; }
if driver=$(cd "$root" && go list -m -f '{{.Dir}}' github.com/mattn/go-sqlite3 2>/dev/null); then
  driver_sqlite=$(sed -n 's/^#define SQLITE_VERSION *"\([^"]*\)"/\1/p' "$driver/sqlite3-binding.h")
  [[ $driver_sqlite == "$sqlite" ]] || { echo "SQLite version mismatch: Go driver $driver_sqlite, extension $sqlite" >&2; exit 1; }
fi
# Cargo requires a single executable for the linker, rather than a command string.
cat > "$work/zig-cc" <<WRAPPER
#!/usr/bin/env bash
exec zig cc -target "$zig_target" "\$@"
WRAPPER
chmod +x "$work/zig-cc"
export RUSTUP_TOOLCHAIN=$rust
export CC="$work/zig-cc"
linker_var="CARGO_TARGET_${rust_target^^}_LINKER"
linker_var=${linker_var//-/_}
export "$linker_var=$work/zig-cc"
make -C "$work/source/core" static CI_GCC="$work/zig-cc" CI_MAYBE_TARGET="$rust_target" rs_build_flags=--locked
archive="$work/source/core/dist/crsqlite-$rust_target.a"
[[ -s $archive ]] || { echo "static library missing: $archive" >&2; exit 1; }
nm --defined-only "$archive" | awk '/[[:space:]]_?sqlite3_crsqlite_init$/ { found=1 } END { exit !found }'
out="$root/.build/crsqlite/$commit/${goos}_${goarch}"
mkdir -p "$out"
cp "$archive" "$out/libcrsqlite.a"
cd "$root/.build/crsqlite"
ln -sfn "$commit" lib
printf 'Built %s\n' "$out/libcrsqlite.a"
