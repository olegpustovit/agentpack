#!/usr/bin/env bash
# Download only checksum-pinned assets; never silently compile from source.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
[[ $# == 2 ]] || { echo "usage: $0 <goos> <goarch>" >&2; exit 2; }
exec python3 - "$root" "$1" "$2" <<'PY'
import hashlib, os, pathlib, re, subprocess, sys, tarfile, tempfile, tomllib
root, goos, goarch = pathlib.Path(sys.argv[1]), sys.argv[2], sys.argv[3]
target = f"{goos}_{goarch}"
if target not in {"linux_amd64", "linux_arm64", "darwin_amd64", "darwin_arm64", "windows_amd64"}:
    sys.exit(f"unsupported cr-sqlite target: {target}")
with (root / "third_party/crsqlite.lock").open("rb") as f:
    pin = tomllib.load(f)
commit = pin["commit"]
if not re.fullmatch(r"[0-9a-f]{40}", commit):
    sys.exit("invalid cr-sqlite source commit")
expected = pin.get("sha256", {}).get(target, "")
if not re.fullmatch(r"[0-9a-f]{64}", expected):
    sys.exit(f"checksum missing from third_party/crsqlite.lock: {target}")
base = root / ".build/crsqlite" / commit
base.mkdir(parents=True, exist_ok=True)
asset = f"crsqlite_{target}.tar.gz"
archive = base / asset

def digest(path):
    with path.open("rb") as f:
        return hashlib.file_digest(f, "sha256").hexdigest()

if not archive.exists() or digest(archive) != expected:
    with tempfile.TemporaryDirectory(dir=base) as scratch:
        downloaded = pathlib.Path(scratch) / asset
        url = f"https://github.com/olegpustovit/agentpack/releases/download/crsqlite-{commit}/{asset}"
        subprocess.run(["curl", "-fL", "--retry", "3", "--connect-timeout", "15", "--max-time", "180", "-o", str(downloaded), url], check=True)
        if digest(downloaded) != expected:
            sys.exit(f"cr-sqlite checksum mismatch: {target}")
        os.replace(downloaded, archive)
# Read a single regular member explicitly; never extract paths or symbolic links.
with tarfile.open(archive, "r:gz") as package:
    members = package.getmembers()
    libraries = [m for m in members if m.name == "libcrsqlite.a"]
    if len(libraries) != 1 or not libraries[0].isfile() or not 0 < libraries[0].size <= 64 * 1024 * 1024:
        sys.exit(f"invalid cr-sqlite library archive: {target}")
    if any(m.name not in {"libcrsqlite.a", "LICENSE"} or not m.isfile() for m in members):
        sys.exit(f"unexpected cr-sqlite archive member: {target}")
    content = package.extractfile(libraries[0]).read()
out = base / target
out.mkdir(exist_ok=True)
library = out / "libcrsqlite.a"
if not library.exists() or library.read_bytes() != content:
    fd, name = tempfile.mkstemp(dir=out)
    staging = pathlib.Path(name)
    try:
        with os.fdopen(fd, "wb") as f:
            f.write(content)
            f.flush()
            os.fsync(f.fileno())
        os.chmod(staging, 0o644)
        os.replace(staging, library)
    finally:
        staging.unlink(missing_ok=True)

link = base.parent / "lib"
if not link.is_symlink() or os.readlink(link) != commit:
    temporary_link = base.parent / f".lib-{os.getpid()}"
    try:
        temporary_link.symlink_to(commit)
        os.replace(temporary_link, link)
    finally:
        temporary_link.unlink(missing_ok=True)
print(f"Verified {target} ({commit})")
PY
