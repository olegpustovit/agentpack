#!/bin/sh
# Build agentpack for every platform on this machine and install it on machines over SSH; no GitHub.
#   scripts/local-release.sh [HOST...]
# Linux and Windows link here with zig. macOS needs Apple's SDK, so it builds on the Mac named by
# AGENTPACK_MAC_BUILDER (default olegs-mac-mini) with a Go toolchain kept in ~/.cache/agentpack-build.
# Each HOST gets the binary for its `uname`, has its gateway stopped, ap (and a real agentpack file)
# replaced, and the gateway started again. Windows hosts are not handled: copy dist/local/ap-windows-amd64.exe.
set -eu
cd "$(dirname "$0")/.."
version=$(sh scripts/build-version.sh)
out=dist/local
mac=${AGENTPACK_MAC_BUILDER:-olegs-mac-mini}
go_version=$(go env GOVERSION)
ldflags="-s -w -X github.com/olegpustovit/agentpack/internal/cli.Version=$version"

make ui-stage
rm -rf "$out" && mkdir -p "$out"
for target in linux/amd64 linux/arm64 windows/amd64; do
    os=${target%/*} arch=${target#*/} flags=$ldflags binary=$out/ap-$os-$arch
    [ "$os" = windows ] && binary=$binary.exe
    [ "$os" = linux ] && flags="$flags -linkmode=external -extldflags=-static"
    ./scripts/fetch-crsqlite.sh "$os" "$arch" >/dev/null
    SOURCE_DATE_EPOCH=0 CGO_ENABLED=1 CGO_LDFLAGS=-lunwind GOOS=$os GOARCH=$arch \
        GOCACHE="$HOME/.cache/agentpack-go/${os}_$arch" CC="$PWD/scripts/cc-crsqlite.sh $os $arch" \
        go build -trimpath -ldflags "$flags" -o "$binary" ./cmd/agentpack
    python3 scripts/verify-smoked-binary.py "$os" "$arch" "$binary"
done

# macOS: the tracked source, the staged UI and the verified crsqlite library, built natively on the Mac.
./scripts/fetch-crsqlite.sh darwin arm64 >/dev/null
{ git ls-files; find internal/network/webdist -type f; find .build/crsqlite -path '*darwin_arm64*' -type f; } |
    sort -u | tar czf "$out/source.tgz" -T -
scp -q "$out/source.tgz" "$mac:/tmp/agentpack-source.tgz"
# Arguments travel on stdin: the Mac's login shell would re-split them on the command line.
{ printf "set -- '%s' '%s'\n" "$go_version" "$ldflags"; cat <<'EOF'
set -eu
build=$HOME/.cache/agentpack-build
if ! "$build/go/bin/go" version 2>/dev/null | grep -q "$1 "; then
    archive=$1.darwin-arm64.tar.gz
    curl -sSfLo "/tmp/$archive" "https://go.dev/dl/$archive"
    want=$(curl -sSf 'https://go.dev/dl/?mode=json&include=all' | /usr/bin/python3 -c "import json,sys; print([f['sha256'] for r in json.load(sys.stdin) for f in r['files'] if f['filename']==sys.argv[1]][0])" "$archive")
    [ "$(shasum -a 256 "/tmp/$archive" | cut -d' ' -f1)" = "$want" ] || { echo "Go archive checksum mismatch" >&2; exit 1; }
    rm -rf "$build/go" && mkdir -p "$build" && tar xzf "/tmp/$archive" -C "$build" && rm "/tmp/$archive"
fi
rm -rf "$build/src" && mkdir -p "$build/src" && tar xzf /tmp/agentpack-source.tgz -C "$build/src" && rm /tmp/agentpack-source.tgz
cd "$build/src"
PATH=$build/go/bin:/usr/bin:/bin:/usr/sbin:/sbin GOTOOLCHAIN=local GOPATH=$build/gopath GOCACHE=$build/gocache GOFLAGS=-modcacherw \
    CGO_ENABLED=1 SOURCE_DATE_EPOCH=0 CC="$PWD/scripts/cc-crsqlite.sh darwin arm64" \
    go build -trimpath -ldflags "$2" -o "$build/ap-darwin-arm64" ./cmd/agentpack
EOF
} | ssh "$mac" /bin/sh -s
scp -q "$mac:.cache/agentpack-build/ap-darwin-arm64" "$out/ap-darwin-arm64"
rm "$out/source.tgz"
(cd "$out" && sha256sum ap-* > checksums.txt)
echo "Built $version in $out"

for host in "$@"; do
    case $(ssh "$host" uname -sm) in
        "Linux x86_64") binary=ap-linux-amd64 ;;
        "Linux aarch64") binary=ap-linux-arm64 ;;
        "Darwin arm64") binary=ap-darwin-arm64 ;;
        *) echo "$host: no binary for this platform" >&2; continue ;;
    esac
    scp -q "$out/$binary" "$host:/tmp/ap-new"
    ssh "$host" /bin/sh -s <<'EOF'
set -eu
bin=$HOME/.local/bin
"$bin/ap" gateway stop >/dev/null 2>&1 || true
for name in ap agentpack; do
    # agentpack is replaced only where the installer left a real file, not a link to ap.
    if [ "$name" = agentpack ] && { [ ! -f "$bin/agentpack" ] || [ -L "$bin/agentpack" ]; }; then continue; fi
    cp /tmp/ap-new "$bin/.$name.new" && chmod 755 "$bin/.$name.new" && mv -f "$bin/.$name.new" "$bin/$name"
done
rm -f /tmp/ap-new
"$bin/ap" gateway start >/dev/null
printf '%s: %s\n' "$(hostname)" "$("$bin/ap" --version)"
EOF
done
