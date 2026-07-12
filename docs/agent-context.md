# Agent Context

Wave 3.1 ships one read-only, allowlisted context projection for local agents:

```sh
scriba context --json
```

The command is JSON-only and publishes `scriba.context.v1`, validated by
[`schemas/context.schema.json`](../schemas/context.schema.json). It contains a
generation timestamp, source status, provider/profile quota windows and
budgets, reset-grant summary, and minimized durable policy events. The current
implementation always uses profile ID `default`.

## Source Selection and Partial Results

Scriba reads the status cache and server store without refreshing a provider.
Claude context comes from the cached status snapshot. Codex uses the newest
valid quota observation between that snapshot and the durable server store;
the store wins ties. Durable Codex history supports budget confidence, and the
policy store supplies up to 20 recent minimized events.

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

- provider IDs and the stable `default` profile ID;
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

## Current Surface Boundary

Wave 3.1 through `e3cd9b2` ships the CLI only. The planned Unix-socket
HTTP/SSE API and stdio MCP adapter are not implemented yet. They must reuse the
same service and prove semantic fixture parity before being described as
available; no API, SSE, or MCP endpoint should be inferred from this contract.

Commit `6a6a163` establishes the undeployed schema-v9 replay foundation. Every
future policy event receives a transactional monotonic replay sequence, and
existing events backfill once in deterministic order. The sequence replaces
unsafe timestamp/hash ordering, but it is not itself an available public API:
the versioned cursor, expiry semantics, SSE adapter, and MCP event tool still
need their shared contract and deployment proof.

Commit `8b6272e` now defines that shared page contract as `scriba.events.v1`.
Its account-free fixed-width cursors support explicit replay, latest-page
inspection, and capture-current tailing. Schema v10 preserves tombstones and
durable high-water state across retention, while malformed rows consume only a
bounded scan slot and never leak raw payloads. The Unix listener primitive at
`fc663de` is also implemented, but HTTP/SSE routes and MCP tools remain absent.
