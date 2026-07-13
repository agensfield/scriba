# Agent Context

Wave 3.1 ships one read-only, allowlisted context projection for local agents:

```sh
scriba context --json
scriba context --json --profile work
```

The command is JSON-only and publishes `scriba.context.v1`, validated by
[`schemas/context.schema.json`](../schemas/context.schema.json). It contains a
generation timestamp, source status, provider/profile quota windows and
budgets, reset-grant summary, and minimized durable policy events. Omitting
`--profile` selects the configured enabled default; an explicit profile must be
one of the enabled stable IDs in config.

## Source Selection and Partial Results

Scriba reads the status cache and server store without refreshing a provider.
Claude context comes from the cached status snapshot. Codex resolves the
selected profile through the durable current profile/account mapping, then
loads only that account's latest observation, budget history, and minimized
policy events. An unrelated newer Codex cache snapshot can never override the
selected account.

Each provider has four independently reported sources: `quota`, `budget`,
`grants`, and `policy-events`. Missing or unreadable inputs do not erase healthy
sources or providers. An unavailable Claude source, for example, can coexist
with complete Codex context. Consumers must inspect each source rather than
treating the envelope as all-or-nothing.

Sources report `available`, `degraded`, or `unavailable`, plus provenance.
Observed data includes `ageMs` and `stale`; observations older than 15 minutes
are stale. `reasonCode`, when present, is one of `stale`, `missing`,
`read_error`, or `history_unavailable`. Fresh available sources omit the reason
code (the schema also reserves `fresh`). Budget entries retain their own
provider-neutral confidence and reason arrays.

## Privacy Boundary

The contract is an allowlist, not a redacted dump. It may expose only:

- provider IDs and the selected stable configured profile ID;
- normalized quota window percentages and reset timestamps;
- derived budget risk, confidence, and reason codes;
- reset-grant count and earliest available expiry;
- minimized policy event IDs, kinds, timestamps, and kind-specific numeric or
  temporal facts;
- source IDs, freshness, availability, and coarse provenance categories.

Raw auth paths or credentials, provider account references, config hashes,
Telegram targets or chat IDs, delivery payloads, policy state bodies, and raw
provider responses are forbidden. Unknown policy event kinds, or known events
missing required safe fields, are omitted rather than passed through.

## Read-Only Semantics

`--cache-dir` and `--state-path` select the existing cache and server database.
Both are opened through read-only SQLite paths. Context reads do not refresh,
configure, send, claim outbox work, or mutate schema or business rows. As with
other live WAL readers, SQLite may perform transient `-wal`/`-shm`
coordination, so the guarantee is semantic read-only behavior, not immutable
sidecar bytes.

## Agent Surfaces

The same service is now exposed through three read-only surfaces:

- `scriba context --json [--profile <id>]` returns `scriba.context.v1`.
- An opt-in owner-only Unix API serves `GET /v1/health`, `GET /v1/context`,
  and replayable SSE at `GET /v1/events`.
- `scriba mcp` runs a stdio MCP server with exactly two tools:
  `scriba_get_context` and `scriba_list_events`.

The Unix API is disabled by default. Enable `server.contextAPI.enabled` in
config, then run `scriba server run`. A missing explicit path resolves to
`context.sock` beside the server database; explicit paths must be absolute.
The socket parent is private (`0700`) and the socket is mode `0600`. This is a
same-UID trust boundary, not isolation from malicious code already running as
the same user. TCP and bearer-authenticated network serving remain absent.

Context and event requests accept `?profile=<id>`; omission selects the enabled
default. MCP accepts the same optional `profile` argument on both tools.
Unknown, disabled, duplicate, empty, or whitespace-padded selections fail with
a bounded code and never fall back to another account.

SSE clients reconnect with `Last-Event-ID` or `?cursor=` and repeat the same
profile selector on reconnect. New connections
capture the current high-water and tail future events; cursor
`v1.0000000000000000` requests retained history. Expired cursors return `410`,
future or malformed cursors return `400`, and heartbeats never advance the
cursor.

Commit `6a6a163` established the schema-v9 replay foundation. Every policy
event receives a transactional monotonic replay sequence, and existing events
backfilled once in deterministic order. The sequence replaces unsafe
timestamp/hash ordering.

Commit `8b6272e` defines the shared page contract as `scriba.events.v1`.
Its account-free fixed-width cursors support explicit replay, latest-page
inspection, and capture-current tailing. Schema v10 preserves tombstones and
durable high-water state across retention, while malformed rows consume only a
bounded scan slot and never leak raw payloads. Commits `29fd69a`, `e157b66`,
and `249991d` implement and expose the Unix API and stdio MCP adapters. A shared
fixture proves context parity across CLI/HTTP/MCP and event/cursor parity across
the service/SSE/MCP while preserving schema, business-row, and privacy bounds.
The schema-v8-to-v10 migration and live transport gate passed at `9bf7392`; the
same surfaces remain healthy on schema 11 in the `v0.3.0` deployment.
