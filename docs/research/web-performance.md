# Gateway and web UI performance

Measured 2026-11-19 on the Linux x86_64 WSL2 development host (i9-12900K). The baseline came from the running gateway via `/proc`; the reproduction used a copy of the same `state.db` (42 MB plus 17 MB WAL) with tailnet sharing off, so no peer traffic is included.

## Results

| Metric | Before | After | Budget |
| --- | ---: | ---: | ---: |
| Idle gateway CPU (no browser, nothing streaming) | 82% of a core | 0.03% | < 1% |
| Idle gateway read I/O | 139 MB/s | 3 KB/s | < 1 MB/s |
| Prompt POST acknowledgement | 2,000 ms (timer) | when the prompt is logged | < 30 ms |
| Added latency per streamed batch | 0–400 ms per hop (poll) | ≤ 16 ms (coalesce) | < 30 ms |
| Worker idle wakeups | 20/s (50 ms flush ticker) | 0 | 0 when not streaming |
| Worker flush transactions while streaming | 20/s | ≤ 4/s | — |
| Browser refetches while a chat streams | 7 query families every 2 s | 0 (chat rows and per-flush session columns name no list) | 0 |
| Network requests before a cached list paints after reload | 6+ serial | 0 (IndexedDB) | 0 |
| Initial JS, gzip | 162.8 KB | 133.3 KB (zod removed, toasts lazy) | ≤ 120 KB (not met) |
| Repeat chat snapshot read, unchanged log | worker encode + gateway buffer + SHA-256 | 304 from the worker's sequence ETag | — |
| React Compiler bailouts | 7 functions | 0 (lint gate) | 0 |
| Event log tail read (`BenchmarkEventLogReadSinceTail`, 20k entries) | linear scan | 62 ns, 1 alloc | — |
| Full-log append with reader wakeup (`BenchmarkEventLogAppendWakesReaders`) | O(n) prepend + re-encode of a head growing to the budget | 1.7 µs, 11 allocs | — |

## Root causes

- **Idle CPU and I/O.** A CPU profile (`/api/debug/pprof/profile`, local CLI only) showed 86% of time in `registry.memoized`: one `crsql_changes` query per Library object, each a scan of every clock table, run twice every 10 s by the Library event topic. Stamps now come from one scan per database version, and the whole index is reused while `crsql_db_version()` holds. Repeated peer pull failures also recorded an error each retry; `issue_counts` is replicated, so each record was a replicated write that woke every cache. A failure is now recorded once until it changes.
- **Write-lock contention.** Every authenticated request updated `auth_sessions`; expiry now slides at most hourly. `state.Open` took 24 `BEGIN IMMEDIATE` locks for migration checks even on a current schema; it now reads the version first. The gateway's sessions and recent-folder reads use its open store.
- **Streaming.** The chat stream polled the event log every 400 ms; `EventLog.Changed()` now wakes readers on append. The worker's persistence loop sleeps until an append and batches for 250 ms. The coalesced oldest chunk is capped at 64 KB so a full log never re-encodes megabytes per token.
- **Browser.** A `state` event refetched seven query families; events now name the changed tables. The chat stream resumes with `since`, appends in O(1) and reduces incrementally (`reduceThread`). Off-screen thread items use `content-visibility: auto`.

## Startup budgets after these changes

Launch, staging and fingerprint code were not changed. Five runs each, medians:

| Benchmark | ARM64 reference host (oracle-vm) | x86_64 dev host | Budget |
| --- | ---: | ---: | ---: |
| `BenchmarkMaterializationHome` | 0.67 ms | 0.34 ms | 1 ms |
| `BenchmarkCodexSetup/cold` | 15.9 ms | 6.96 ms | 20 ms |
| `BenchmarkCodexSetup/warm` | 0.32 ms | 0.13 ms | 1 ms |

## Deployed gateways

Measured over 60 s after deploying, with live sessions and mesh peers: the development gateway reads 93 KB/s at 0.45% CPU (it was 1.6 MB/s until the Library cache keyed on the registry clock tables instead of the database-wide version, which every streamed flush moves). oracle-vm reads 55 KB/s at 0.43% CPU. The public gateway serves `/sw.js`; a fresh gateway driven in Chromium registered it, cached the shell and every first-paint asset, painted an offline reload in 45 ms and never served `/api/` from cache. `/api/debug/pprof/` answers 401 from the public URL. Against a real Claude agent through the development gateway, the prompt POST was acknowledged in 13 ms (it always took 2,000 ms), a second prompt sent while the agent worked was queued in 3 ms, and both replies arrived through the pushed stream.

## After the audit milestones (2026-11-20)

Same x86_64 development host and reproduction (copied `state.db` of 163 MB plus 17 MB WAL, sharing off, 60 s after a 15 s warm-up), with machine detail, worktrees, git state, notifications and Web Push merged:

| Metric | Result | Budget |
| --- | ---: | ---: |
| Idle gateway CPU | 0.05% | < 1% |
| Idle gateway read I/O | 5 KB/s | < 1 MB/s |
| `GET /api/machine`, first call (seven agent CLIs probed in parallel) | 463 ms | — |
| `GET /api/machine`, repeat (versions cached per binary path, size and mtime) | 0.7 ms | — |
| `GET /api/version/latest` (GitHub, then cached 15 min for display and updater) | 290 ms | — |
| `BenchmarkMaterializationHome` / `BenchmarkCodexSetup/cold` / `warm`, medians of three | 0.29 / 7.5 / 0.12 ms | 1 / 20 / 1 ms |

The push sender polls every 3 s only while a subscription exists; with none it does no work. Not re-measured on the ARM64 reference host.

## Reproduce

```sh
go test ./internal/acp -run '^$' -bench 'EventLog' -benchmem -benchtime=20000x
cd ui && bun run lint   # includes scripts/check-compiler.ts
```

Idle gateway: copy `state.db` and `state.db-wal` to an empty `AGENTPACK_HOME`, run `ap gateway sharing off` and `ap gateway serve --listen 127.0.0.1:0` there, wait 15 s, then sample `rchar` in `/proc/<pid>/io` and `utime+stime` in `/proc/<pid>/stat` over 60 s. Profile with the local secret from `network/node.json`:

```sh
curl -H "Authorization: Bearer $SECRET" "http://$ADDRESS/api/debug/pprof/profile?seconds=30" -o cpu.pb.gz
go tool pprof -top -cum ap cpu.pb.gz
```

## Not done

- `synchronous=NORMAL` for `state.db`: it can lose the last commits on power loss, including replicated changes a peer already acknowledged.
- Merging adjacent token chunks into one stored row: it changes the log's seq contract with the UI and peers.
- Collapsing diffs in older turns: the thread's contract (`conversation.spec.ts`) keeps diffs and terminal output open on load; `content-visibility` already skips their layout and paint off screen.
- Rows carried inside events: the browser list is the mesh-wide `/api/sessions` view with each worker's live terminal state merged in, which the gateway event does not have. With per-table events, a list refetch happens only when a list column changed, and is one gzipped request. The IndexedDB sync feed (layer C) was conditional on A and B missing budget; the remaining miss is bundle size, which layer C would not fix.
- The 120 KB initial JS target: what remains is `react-dom` (~45 KB), TanStack Router (~25 KB), TanStack Query (~13 KB), `cn` class merging (~13 KB) and base-ui's button. `cn build` compiled tables were tried and came out larger (14.7 KB). Moving `cn` behind the root route would only hide it from the metric: the shell needs it for the same paint.

## Conversation checks after the 2026-11-21 changes

The Chromium and WebKit runs use a local production build and mocked gateway
responses. Across both engines, a 120-turn fixture opens on the latest reply
with fewer than 50 thread rows mounted. Scrolling to the top reveals the first
turn. A separate check holds the permission and archive responses and verifies
that the controls update before the gateway answers, then roll back on rejection.
The IndexedDB check verifies that the cache writes changed queries and excludes
the live chat.

```sh
cd ui
bun run build
bun run e2e --project=chromium --project=webkit -g 'long conversation starts|offline cache writes|failed permission answer|archive hides'
```

A separate switch benchmark opens a second 120-turn chat after the app and the
first chat have loaded. It measures from the link click to the first animation
frame where the final reply is inside the scroll viewport. Five isolated
browser contexts ran for each engine and revision on the Linux x86_64
development host. Chromium uses 4× CDP CPU throttling. WebKit runs at native
speed.

| Engine | Before (`f6e7f4b`) | After (`f6ee619`) | Mounted conversation paragraphs |
| --- | ---: | ---: | ---: |
| Chromium | 410 ms | 189 ms | 360 → 34 |
| WebKit | 147 ms | 76 ms | 360 → 34 |

Build both revisions with `bun run build` in their respective UI directories.
Start a Vite preview for each build on a separate localhost port, then run the
benchmark from the current UI directory.

```sh
bun run scripts/benchmark-conversation.ts http://127.0.0.1:4230 http://127.0.0.1:4231
```

The browser Performance timeline also retains the latest
`agentpack:chat-first-frame` and `agentpack:prompt-confirmed` measurements.
The first ends when the chat is ready for its next paint. The second ends when
the request ID appears in the conversation log. Both contain only timings.
