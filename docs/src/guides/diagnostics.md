# Diagnostics

Agentpack records structured diagnostics on the machine running the gateway or
worker. Use **System → General → Diagnostics**, the `ap logs` commands, or the gateway's
MCP tools to investigate failures and record fixes. No external telemetry service
receives these events.

## What is recorded

Events include time, duration, trace/span IDs, machine and process, operation,
status, session ID when relevant, and the app version, commit, OS, architecture
and state-schema version. Failures include a redacted error chain and stack.
Gateway requests record the matched route, status and response size. Workers
record starts, stops, restarts and unexpected exits. ACP turns record timings,
sequence ranges and counts of tools and permission requests. Browser errors
include the loaded UI build ID, route, viewport, and bounded navigation/action/API
breadcrumbs.

Successful foreground launches open no diagnostic files or state database. A CLI
failure writes a spool event; harness arguments are omitted. The gateway ingests
completed spool files after two minutes. `ap logs` also reads unconsumed spools
directly, so diagnostics remain available with the gateway stopped.

If a Main gateway is configured, other explicitly approved mesh machines forward
failed traces to it in bounded batches. Successful spans sharing a failed trace
are included; healthy traces stay local. Uploads preserve the original machine
identity and resume without duplicates after disconnection or restart. Main keeps
the same five-day retention. See [Main gateway](../concepts/state.md#main-gateway-and-archive).

## What is excluded

Diagnostics exclude prompt/reply text, tool arguments/results, HTTP bodies,
passwords, tokens, cookies, credential headers and raw environment values. Browser
breadcrumbs omit form values and conversation text. Values, stacks and event size
are bounded, and credential-shaped text is redacted at recording and ingestion.

ACP diagnostic stderr has a small redacted tail. Terminal sessions merge stderr
with stdout, so their terminal transcript is never copied into diagnostics; the
event explicitly reports that a separate stderr stream is unavailable. Errors can
still contain project paths or local identifiers. Bundle exports additionally
remove absolute paths, URLs, private names and user identities. Review an export
before sharing it.

## Inspect and resolve

```sh
ap logs issues --status open --json
ap logs show FINGERPRINT
ap logs trace TRACE_ID --json
ap logs trace TRACE_ID --all-nodes --json
ap logs query --since 2h --kind http --level error --limit 100
ap logs query --session SESSION_ID --node NODE_ID --grep timeout
ap logs resolve FINGERPRINT --version v0.14.2 --note "Handle an empty session"
ap logs reopen FINGERPRINT
ap logs ignore FINGERPRINT
ap logs bundle FINGERPRINT -o ./repro
```

Trace reads merge local events with Main while the local gateway is running.
`--all-nodes` also asks reachable approved peers; unavailable machines do not
prevent reading retained copies. With the gateway stopped, local traces and
unconsumed spools remain readable. The MCP `get_trace` tool has the same default
merge and an optional `all_nodes` flag. It returns up to 16 events and a
`next_cursor`; pass that value as `cursor` until it is absent. Any unreachable
requested peers appear in `unavailable_nodes`. The HTTP equivalent is
`GET /api/logs/traces/TRACE_ID?page=1&after=CURSOR`. The CLI follows every page
automatically, including traces larger than one network response.

Every command accepts `--json`. `issues --status ''` includes all statuses.
`query` accepts only structured flags; `--grep` searches literal text and raw SQL
is not accepted. `--since` accepts a positive duration or an RFC3339 timestamp.
The default query limit is 100, with a maximum of 10,000. `trace` returns every
retained event for that trace in time order.

Issue counts include every occurrence even when repeat samples are thinned. A
resolved issue records who resolved it, when, a note and the version containing
the fix. A later occurrence from that version or a newer one reopens the issue
and marks it **Regressed**. Occurrences from older builds are counted without
reopening it. Delayed errors that happened before resolution also leave it
resolved. Unparseable development versions are treated conservatively as newer.
Ignored issues stay ignored until explicitly reopened or resolved.

MCP tools `list_issues`, `get_issue` and `get_trace` are read-only;
`resolve_issue` records a fix version and optional note. They use the gateway's
existing authentication and API checks.

## Files and source maps

Under `AGENTPACK_HOME`, `logs/YYYY-MM-DD.db` contains plain SQLite day logs,
deduplicated stacks and local counters. `logs/spool/` holds NDJSON from processes
that do not own the gateway writer. `state.db` holds issue metadata and machine
counts, which outlive the short raw-log window. Directories are private and data
files use owner-only permissions. The gateway removes day files older than five
days. It stops accepting ordinary events at the 256 MiB daily budget while
preserving error samples; repeated errors are sampled after the first 20 per
fingerprint per hour, then one in 50. The spool budget is 16 MiB, oldest files first.

A bundle creates a new private directory containing `issue.json`,
`traces.ndjson`, `rows.json` and `versions.json`; existing destinations are refused.
It includes the complete retained traces involved in the issue. Session and
mesh-node records are included when their state tables exist; unavailable tables
are explicitly identified. Public UI asset filenames and build IDs are preserved.

`ap logs show` prints the UI build ID and asset names for a browser error. Match
them to `ui-sourcemaps_<version>.tar.gz` attached to that app release. Source maps
are separate release assets: they are neither embedded in the executable nor
served to browsers. A tab left open during deployment retains the ID of the UI
it actually loaded.
