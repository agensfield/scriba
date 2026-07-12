# Scriba Current State

Date: 2026-07-12

Scriba is the first child project under Agensfield. It is a fast, minimal
Claude Code and Codex usage tracker.

## Current Shape

- Go CLI at `cmd/scriba`.
- Implementation under `internal/`.
- The existing Swift/AppKit/SwiftUI menu bar app remains under `apps/macos` but
  is outside the active roadmap and will not receive parity or packaging work.
- SQLite derived cache under `~/.cache/scriba` by default.
- JSON status snapshot supports `scriba status --fast`.
- Codex local reports preserve full traffic while exposing effective tokens
  (`input - cached input + output`) separately. Report totals derive from input
  plus output instead of trusting inconsistent JSONL `total_tokens` values.
- Codex reports calculate standard-tier API-equivalent cost for GPT-5.4,
  GPT-5.5, and GPT-5.6 Sol/Terra/Luna. GPT-5.6 pricing is selected per request,
  with the whole request switching to long-context rates above 272K input.
- Human reports show up to three exact model names, so secondary GPT-5.6 models
  are no longer hidden behind the dominant model. JSON keeps every model.
- Daily/weekly/monthly reports and status use the configured/system timezone;
  `--timezone` provides a per-command override and JSON records the resolved
  zone.
- The Codex parsed-event cache key is versioned with scanner semantics, so an
  upgrade cannot silently reuse incompatible historical events.
- Codex token payloads decode integers exactly beyond JavaScript's safe-integer
  boundary and reject fractional/overflow counters. Claude counters are clamped
  nonnegative without conflating cache-read tokens with uncached input.
- Both provider parser caches use explicit semantic namespaces. Frozen corpus
  fixtures, cumulative-reset properties, and native fuzzers guard parsing.
- Stable `scriba.v1` status, Codex limits, profile, and reset-grant JSON outputs,
  plus `scriba.budget.v1` reports, have checked-in Draft 2020-12 schemas and
  canonical validation goldens.
- Wave 3.1 shared agent context CLI is deployed through commit `e3cd9b2`.
  `scriba context --json` emits the allowlisted `scriba.context.v1` projection
  from read-only cache/store inputs, with independent freshness and absence per
  source, the `default` profile, and minimized durable policy events. The
  owner-only Unix HTTP/SSE API and two-tool stdio MCP adapter are implemented
  and cross-surface parity-tested through `e659570`, but remain undeployed until
  the copied-live schema-v8 to v10 migration and rollback gate passes.
- `scriba codex budget` and `scriba claude budget` derive provider-neutral quota
  pacing from fresh provider windows. They use percentage points rather than
  local token counts and intentionally have no `--fast` mode. Codex may use up
  to 24 hours of matching durable observations; Claude is current-cycle only.
- Pricing is embedded from a hash-bound reviewed offline catalog. Maintainer
  refresh writes a candidate only; CI validates provenance, aliases, rates,
  tier thresholds, boundary goldens, and deterministic generation without
  network access.
- The macOS app resolves a system `scriba` when same/newer than the bundled
  helper, otherwise it uses the bundled native Go helper.
- The macOS app exposes used/remaining display mode, menu bar text mode, and
  refresh cadence settings.
- Telegram alert config is shared between `scriba config telegram` and the
  macOS settings window.
- The app uses controlled native menu rows and macOS 26 glass button styles in
  the settings window when available.
- Packaging supports host-arch and universal app/helper builds. Debug builds use
  a separate bundle identifier from release builds.
- `scriba server run` is the resident devbox process for live Codex limit
  polling, reset detection, Telegram commands, limit/grant warning
  notifications, health, stats, pruning, and radar probability alerts.
- Codex limit polling reads explicit additional-rate-limit buckets, including
  Spark, and exposes the available reset-grant count plus earliest available
  grant expiry when the ChatGPT backend exposes reset-credit metadata.
- Telegram reset-grant expiry alerts are tracked per available grant credit and
  fire once at the 5-day, 3-day, and 1-day checkpoints before each credit's own
  `expires_at`.
- Telegram `/grants` and its dedicated inline keyboard button render every
  available reset grant from the latest durable server observation, including
  title, type, status, granted time, expiry, remaining lifetime, and full id.
- Schema v7 stores reset, limit-warning, grant-warning, reset-grant, and Radar
  notification intents in one canonical outbox, atomically with their typed
  business events. Telegram claims one target-filtered row at a time with a
  fenced lease; legacy delivery tables remain read-only migration evidence.
- The live schema-v8 migration adds constrained `policy_states` and
  `policy_events` tables, account/policy lookup indexes, semantic-event
  uniqueness, schema validation, and refusal to open a future schema version.
  Downgrading a v8 database is restore-only: use a pre-migration backup with an
  older binary rather than attempting an in-place rollback.
- Commit `6a6a163` adds the undeployed schema-v9 durable replay sequence.
  `policy_event_replay` maps every policy event to a transactional,
  never-reused monotonic ordinal through an insert trigger. Replay pages use a
  single read snapshot and a captured high-water mark, so later/backdated
  inserts cannot be skipped or leak across a page boundary. Commit `8b6272e`
  adds schema v10 tombstones plus strict `scriba.events.v1` cursor paging;
  `fc663de` adds the reviewed Unix socket ownership primitive. Commits
  `29fd69a` through `e659570` add the Unix HTTP/SSE and stdio MCP transports,
  supervised CLI/server wiring, exact response contracts, and real lifecycle
  and parity tests. Profiles move to server schema v11. Wave 3.1 is deployed at
  `9bf7392` on live schema 10 with the owner-only context API enabled.
- A fresh verified online schema-v8 backup passed the disposable schema-v10
  rehearsal through `0d97c27`: idempotence, integrity, replay/high-water,
  unchanged business-table data, older-binary refusal, and untouched-v8
  restore-copy opening all passed. The authoritative stopped-service proof and
  activation receipt followed in
  [`schema-v10-migration.md`](schema-v10-migration.md).
- The authoritative stopped-service schema-v8 backup and live schema-v10
  cutover subsequently passed. Unix HTTP/SSE and both stdio MCP tools passed
  privacy, restart, and journal smokes. OpenAI's temporary removal of the
  five-hour bucket is handled by a default-on compatibility flag with
  `SCRIBA_FEATURE_CODEX_TEMPORARY_NO_FIVE_HOUR=false` as the kill switch.
  Weekly-only responses do not fabricate five-hour resets, warnings, budgets,
  or agent context, and the last durable five-hour state is preserved.
- The resident server now evaluates the closed policy kinds
  `remaining_checkpoint`, `reset_transition`, `grant_available`, and
  `grant_expiry_checkpoint`. Its `current` preset preserves the existing
  thresholds. The live cutover bootstrapped 15 policy states without historical
  replay or events, and subsequent polls evaluate state and enqueue any new
  semantic events atomically through the canonical outbox.
- Read-only `policy validate`, `policy list`, `policy explain`, and `outbox
  list` inspection surfaces are deployed on the devbox at commit `a168e34`.
  State-backed queries use a
  read-only SQLite open, support bounded exact filters, offer field-aware
  redaction, and publish the typed `scriba.policy-validate.v1`,
  `scriba.policy-list.v1`, `scriba.policy-explain.v1`, and
  `scriba.outbox-list.v1` JSON contracts.
- The Wave 2 release gate is closed at `6bfbcb3`. Composed fixtures prove all
  four policy transition kinds persist through the typed ledgers and canonical
  outbox with identical payloads, and a real SQLite/Telegram SDK test proves
  failed delivery backoff and successful retry of the same row.
- The Wave 3.1 devbox receipt uses the Linux amd64 artifact with SHA-256
  `b57cd17bc2dc26a557abdc558e2f3e96f6b7fc2b69a2126f3f630a73df1bf467`.
  The service was active and healthy at `e3cd9b2`, schema 8. A context smoke
  returned `scriba.context.v1`, eight sources, Codex under `default`, zero
  events, and honest unavailable/missing Claude sources. Two context reads left
  main/cache database hashes and schema/policy/outbox counts unchanged
  (`8|15|0|40|41`); the forbidden-data grep was clean.
- Telegram `getUpdates` batches are durably staged as exact raw JSON before the
  polling dependency can advance its cursor. Pending updates replay after a
  crash, while malformed updates dead-letter visibly.
- Server stats and health expose outbox/inbox backlog, due work, attempts,
  oldest pending age, expired leases, and dead letters.
- `scriba codex summary` appends live Codex limit/reset-grant metadata unless
  `--no-remote` is passed.
- `scriba codex reset-grants` shows every available reset grant and each
  grant's own expiration timestamp.
- `scriba codex profile` reads the ChatGPT/Codex profile backend
  (`/backend-api/wham/profiles/me`) and renders profile token activity,
  streaks, reasoning mix, thread/skill counts, daily/weekly activity bars, and
  top skills/plugins while preserving all buckets with `--json`.
- `scriba update --check` compares the local binary to the latest GitHub tag;
  `scriba update` uses `go install` for regular installs and refuses
  Homebrew-managed binaries in favor of `brew upgrade scriba`.
- `internal/radar` reads `https://codexradar.com/current.json` first and falls
  back to the old `https://codex-reset-radar.pages.dev/current.json` endpoint.

## Current Shipping Lane

The devbox server and Telegram bot lane is implemented:

- `scriba server run` as one resident process.
- Live Codex backend limit polling from local Codex auth.
- SQLite server state.
- Weekly reset detection from `resetsAt` timestamp advances.
- Telegram long polling, commands, reset notifications, low-limit warnings,
  reset-grant expiry warnings, health/recovery alerts, stats, and radar
  probability milestone alerts.
- Radar probability alerts fire on upward 24h probability checkpoint crossings
  at 25%, 50%, and 75%; drops update the stored checkpoint silently so later
  increases can alert again.

Canonical spec:

- `/Users/arda/Documents/obsidian/obsidian-main/projects/agensfield/scriba/server-telegram-spec.md`

## Invariants

- Claude/Codex logs and provider APIs remain source of truth.
- Scriba stores only read-only derived state.
- Cache deletion is safe.
- Normal local scans read JSONL line-by-line.
- Human output is default; `--json` is explicit for agents and automation.
- `effectiveTokens` and `totalTokens` are distinct contracts: the former
  excludes cache reads, while the latter represents full model traffic.
- Calculated `costUSD` is an estimated standard API equivalent, not ChatGPT
  subscription billing.
- `--no-remote` skips provider API probes.
- `--fast` reads cached status only.
- `--redact` removes share-sensitive paths and identifiers from JSON output.
- Terminal metric rows use a shared label column per rendered provider/output so
  progress bars align across short and long labels.
- Radar alerts are derived notification state; Codex Radar JSON remains the
  source of truth.
- Server unit tests keep radar polling opt-in; the real resident server wires a
  live radar client in `scriba server run`.

## Verification

Preferred core gate:

```sh
go test ./...
go vet ./...
staticcheck ./...
golangci-lint run ./...
gosec ./...
govulncheck ./...
```

Optional macOS menu bar gate:

```sh
swift test --package-path apps/macos
apps/macos/Scripts/package_zip.sh release
```

## Open Follow-Ups

- Execute the accepted local usage control-plane program in
  [`control-plane-roadmap.md`](control-plane-roadmap.md). Reliability,
  migration safety, the canonical outbox, budget surfaces, and the schema-v8
  policy runtime, deployed inspection CLI surfaces, the Wave 3.1 context CLI,
  and local API/SSE/MCP parity are completed checkpoints. Agent-transport
  deployment, profiles, and remaining surface parity remain.
- Tighten Go regression tests around frozen TS-era fixtures.
- Revisit Claude `blocks` for strict bounded-memory behavior.
- Decide whether ad-hoc signed zip distribution is enough before
  notarization/Sparkle.
