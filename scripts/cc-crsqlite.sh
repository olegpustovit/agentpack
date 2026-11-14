#!/usr/bin/env bash
# GoReleaser/Go CC entrypoint; Darwin builds use an Apple SDK on a Mac runner.
set -euo pipefail
goos=$1
goarch=$2
shift 2
case "$goos/$goarch" in
  darwin/amd64) exec xcrun clang -arch x86_64 "$@" ;;
  darwin/arm64) exec xcrun clang -arch arm64 "$@" ;;
  linux/amd64) target=x86_64-linux-musl ;;
  linux/arm64) target=aarch64-linux-musl ;;
  windows/amd64) target=x86_64-windows-gnu ;;
  *) echo "unsupported C compiler target: $goos/$goarch" >&2; exit 2 ;;
esac
exec zig cc -target "$target" "$@"
