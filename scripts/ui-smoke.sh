#!/usr/bin/env bash
# Starts the built gateway with a throwaway AGENTPACK_HOME and asserts the embedded UI and API basics.
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
bin="$root/bin/agentpack"
webdist="$root/internal/network/webdist"
home=$(mktemp -d)
port=$((20000 + RANDOM % 20000))
base="http://127.0.0.1:$port"
log="$home/gateway.log"
pid=""

cleanup() {
  if [[ -n "$pid" ]]; then kill "$pid" 2>/dev/null || true; wait "$pid" 2>/dev/null || true; fi
  rm -rf "$home"
}
trap cleanup EXIT

fail() {
  echo "ui-smoke: FAIL: $*" >&2
  tail -20 "$log" >&2 2>/dev/null || true
  exit 1
}

[[ -x "$bin" ]] || fail "$bin missing; run make build"
asset=$(cd "$webdist/assets" 2>/dev/null && ls *.js | head -1) || fail "no built JS asset in $webdist; run make ui-stage"
[[ -n "$asset" ]] || fail "no built JS asset in $webdist; run make ui-stage"

AGENTPACK_HOME="$home" "$bin" gateway serve --listen "127.0.0.1:$port" >"$log" 2>&1 &
pid=$!
for _ in $(seq 1 100); do
  curl -fsS "$base/healthz" >/dev/null 2>&1 && break
  kill -0 "$pid" 2>/dev/null || fail "gateway exited during startup"
  sleep 0.1
done
curl -fsS "$base/healthz" >/dev/null || fail "/healthz not ready"
secret=$(sed -n 's/.*"secret":"\([^"]*\)".*/\1/p' "$home/network/node.json")
[[ -n "$secret" ]] || fail "no local secret in network/node.json"

# get <path> [curl args...] sets code, headers (lowercased) and body file.
get() {
  local path=$1
  shift
  code=$(curl -sS -o "$home/body" -D "$home/headers" -w '%{http_code}' "$@" "$base$path")
  headers=$(tr -d '\r' <"$home/headers" | tr 'A-Z' 'a-z')
}
header() { grep -i "^$1:" <<<"$headers" | head -1 | cut -d' ' -f2- || true; }
expect_code() { [[ "$code" == "$1" ]] || fail "$2: HTTP $code, want $1"; }
expect_header() {
  local value
  value=$(header "$1")
  [[ "$value" == *"$2"* ]] || fail "$3: $1 is '$value', want it to contain '$2'"
}

for path in / /sessions/local/x?group=project /library/package/github.com/a/b; do
  get "$path"
  expect_code 200 "GET $path"
  expect_header content-type text/html "GET $path"
  expect_header cache-control no-cache "GET $path"
  grep -q '<div id="root">' "$home/body" || fail "GET $path: index.html is not the built UI"
  grep -q 'Agentpack UI was not built' "$home/body" && fail "GET $path: served the placeholder"
done

get "/assets/$asset"
expect_code 200 "GET /assets/$asset"
expect_header content-type text/javascript "GET /assets/$asset"
expect_header cache-control "public, max-age=31536000, immutable" "GET /assets/$asset"

get "/assets/$asset" -H "Accept-Encoding: gzip"
expect_code 200 "GET /assets/$asset gzip"
expect_header content-encoding gzip "GET /assets/$asset gzip"
expect_header vary accept-encoding "GET /assets/$asset gzip"
[[ "$(head -c2 "$home/body" | od -An -tx1 | tr -d ' ')" == "1f8b" ]] || fail "gzip body is not gzip"

get /assets/missing.js
expect_code 404 "GET /assets/missing.js"
[[ "$(header content-type)" != *text/html* ]] || fail "GET /assets/missing.js returned HTML"

for path in /icon.svg /manifest.webmanifest; do
  get "$path"
  expect_code 200 "GET $path"
  expect_header cache-control no-cache "GET $path"
done
expect_header content-type application/manifest+json "GET /manifest.webmanifest"

get /missing.png
expect_code 404 "GET /missing.png"

get /anything -X POST -H "Authorization: Bearer $secret"
expect_code 405 "POST /anything"

get /api/status
expect_code 200 "GET /api/status"
expect_header content-type application/json "GET /api/status"

get /api/nope -H "Authorization: Bearer $secret"
expect_code 404 "GET /api/nope"
expect_header content-type application/json "GET /api/nope"

echo "ui-smoke: ok ($asset)"
