#!/usr/bin/env python3
"""Publishing must reproduce the exact binary that passed the platform smoke job."""
import hashlib
import os
import pathlib
import sys
import tarfile
import zipfile

approved = os.environ.get("AGENTPACK_SMOKED_DIR")
if approved:
    goos, arch, binary_path = sys.argv[1:]
    extension = "zip" if goos == "windows" else "tar.gz"
    archives = list(pathlib.Path(approved).glob(f"agentpack_*_{goos}_{arch}.{extension}"))
    if len(archives) != 1:
        sys.exit(f"missing unique smoke-tested archive for {goos}/{arch}")
    binary = "ap.exe" if goos == "windows" else "ap"
    if extension == "zip":
        with zipfile.ZipFile(archives[0]) as package:
            expected = package.read(binary)
    else:
        with tarfile.open(archives[0]) as package:
            expected = package.extractfile(binary).read()
    with open(binary_path, "rb") as actual:
        digest = hashlib.file_digest(actual, "sha256").digest()
    if digest != hashlib.sha256(expected).digest():
        sys.exit(f"refusing to publish untested binary: {goos}/{arch}")
