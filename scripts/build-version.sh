#!/bin/sh
set -eu

if tag=$(git describe --tags --exact-match --match 'v[0-9]*' --exclude '*-hotfix*' HEAD 2>/dev/null) &&
    git diff-index --quiet HEAD --; then
    printf '%s\n' "${tag#v}"
    exit
fi

if base=$(git describe --tags --abbrev=0 --match 'v[0-9]*' --exclude '*-*' HEAD 2>/dev/null); then
    count=$(git rev-list --count "$base..HEAD")
    version=$(printf '%s\n' "${base#v}" | awk -F. '{print $1 "." $2 "." ($3+1)}')
else
    count=$(git rev-list --count HEAD)
    version=0.0.1
fi
sha=$(git rev-parse --short HEAD)
dirty=
if ! git diff-index --quiet HEAD --; then
    dirty=.dirty
fi
printf '%s-dev.%s+%s%s\n' "$version" "$count" "$sha" "$dirty"
