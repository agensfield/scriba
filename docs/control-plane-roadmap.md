# Scriba Control-Plane Roadmap

Status: accepted for execution on 2026-07-12

Scriba is evolving from a local usage reporter and Telegram bot into a
local-first usage control plane for terminal agents. The product remains one
Go process backed by SQLite. Provider logs and APIs remain authoritative;
Scriba stores derived observations, decisions, events, and delivery state.

This roadmap is dependency ordered. A later phase does not begin until the
preceding phase's gate passes and its migration/rollback receipt is recorded.

## Product Contract

Scriba should answer four questions reliably:

1. What quota and reset state exists now?
2. At the observed pace, when will capacity run out?
3. Which deterministic policy events follow from that state?
4. How can humans and local agents consume those facts safely?

It must not claim that local token traffic maps to subscription quota capacity.
Pacing uses provider quota percentage points unless a provider exposes a
tested denominator.

## Architectural Spine

```text
provider adapters
  -> normalized observations
  -> deterministic budget decisions
  -> typed policy events
  -> canonical delivery outbox
  -> Telegram | signed webhook | ntfy
  -> context CLI | local API/SSE | MCP
```

The provider, budget, policy, delivery, and presentation packages have one-way
dependencies. Telegram, HTTP, MCP, and macOS are adapters, not sources of
business rules.

## Binding Decisions

- Keep SQLite, one resident process, and read-only provider behavior.
- Preserve typed event tables during migration; replace the five duplicated
  delivery ledgers with one canonical outbox once, in Wave 1.
- Take and verify a restorable backup before the outbox schema migration.
- Use at-least-once provider delivery with atomic local enqueue, fenced claims,
  deterministic IDs, bounded retries, and visible dead letters. Telegram
  cannot provide exactly-once sends.
- Build a closed-world typed policy engine. Do not embed CEL, OPA, JSONLogic,
  shell actions, or user-defined message templates.
- Build one allowlisted agent-context query service and reuse it across CLI,
  HTTP/SSE, and MCP.
- Default the local API to a Unix socket. TCP is explicit, loopback-only, and
  authenticated. MCP is stdio-only in this program.
- Add profiles without mutating process-wide `CODEX_HOME`; auth paths are
  explicit per profile.
- Do not build a cloud dashboard, public API, provider marketplace,
  microservices, autonomous grant redemption/model switching, or an LLM advice
  layer.

## Phase 0: Planning and Evidence

Deliverables:

- Reconcile repository, vault, runtime, and upstream reference truth.
- Record this roadmap and its planning receipt before implementation.
- Keep a copy of a production-like schema-v6 database for migration tests,
  with secrets and account identifiers removed where fixtures are committed.

Gate:

- Clean `main`, synchronized vault, accepted dependency order, and explicit
  blockers below.

## Wave 1: Reliability Kernel and Contract Lab

### 1.0 CI and recoverability

- Add cross-platform CI for Go tests/race/vet, pinned lint/security tools,
  Swift tests, and packaging smoke.
- Add `scriba server backup` using SQLite `VACUUM INTO`, candidate
  `quick_check`, checksums, owner-only permissions, and count retention.
- Add a documented restore drill. Restore automation follows only after the
  backup format and failure behavior are proven.

Gate: a live-WAL fixture backs up and restores without mutating the source.

### 1.1 SQLite, OAuth, network, and process safety

- Apply `foreign_keys` and `busy_timeout` through the SQLite DSN on every
  connection; make pool limits explicit and require WAL initialization.
- Make Telegram offsets monotonic and handlers serial so the stored cursor is
  a contiguous completed prefix.
- Serialize Codex OAuth read-refresh-write across Scriba processes, preserve
  unknown auth fields, detect non-cooperating external writers, and durably
  replace the auth file.
- Bound all resident HTTP calls and refresh operations.
- Persist poll-attempt state, distinguish poll/data health from process
  liveness, propagate fatal child errors, and make clean shutdown bounded.

Gate: race/timeout/subprocess tests pass repeatedly; failures are durable and
systemd can restart an unexpectedly exited process.

### 1.2 Canonical durable outbox

- Keep the existing typed reset, warning, grant, and radar event tables.
- Introduce one canonical notification envelope and delivery outbox.
- Enqueue eligible event plus outbox rows in the same transaction.
- Preserve silent baseline reset-grant eligibility during migration.
- Claim with one conditional update and a random fencing token; stale workers
  cannot finish another worker's lease.
- Increment attempts at claim time, use deterministic backoff, dead-letter
  after the accepted maximum, and expose backlog/dead letters in health/stats.
- Backfill only rows proven notification-eligible. Do not replay history to a
  newly configured target.
- Drain legacy delivery rows through a compatibility reader, then retain old
  tables as read-only migration evidence for at least one release.

Gate: fault-injection proves no eligible event can commit without an outbox
row; concurrent claim tests produce one owner; copied schema-v6 migration,
rollback, `quick_check`, and `foreign_key_check` pass.

### 1.3 Provider contract laboratory

- Add a synthetic frozen corpus covering malformed/truncated/oversized JSONL,
  cumulative counter resets, model conflicts/switches, replay/fork markers,
  numeric boundaries, cache invalidation, DST folds/gaps, and long-context
  pricing boundaries.
- Use typed JSON decoding with exact numbers and explicit model precedence.
- Version both provider parser caches.
- Publish Draft 2020-12 schemas for stable public JSON envelopes.
- Add fuzz/property/golden tests and offline frozen differential projections.
- Add a reviewed pricing-catalog refresh workflow; runtime commands remain
  offline and reproducible.

Gate: fixtures are the Scriba contract, not ccusage/openusage; required PR
checks are network-independent and all existing public outputs remain
compatible or are deliberately versioned.

Wave 1 release gate: full Go/Swift checks, copied production migration,
backup/restore drill, devbox deploy, Telegram command smoke, and a two-poll
soak with no duplicate/lost delivery or SQLite lock errors.

## Wave 2: Budget Intelligence and Policy

### 2.1 Deterministic quota pacing

Status: code-complete checkpoint. Both budget commands, typed
`scriba.budget.v1` output, schema validation, history bounds, and deterministic
tests are committed. This does not imply a devbox deployment receipt.

- Add a pure provider-neutral budget package with an injected clock.
- Calculate remaining quota percentage points, cycle/recent burn, safe hourly
  and daily allowance, projected exhaustion, temporal margin, risk,
  freshness, and categorical confidence.
- Use stored Codex window history when available. Claude starts with honest
  current-cycle confidence until durable history exists.
- Keep derived budgets out of SQLite; recompute them from observations.
- Add `scriba codex budget` and `scriba claude budget` with typed JSON and
  explicit epistemic reason codes.

Gate: fixed-clock, property, fuzz, and rendering tests prove deterministic
ordering, no NaN/Inf, monotonic projections, and zero influence from local
token counts on quota calculations.

### 2.2 Typed policy evaluation

Status: policy-core/schema checkpoint only. Schema v8 and the closed-world pure
evaluator are committed; resident-server cutover, policy CLI inspection
surfaces, live migration/deploy proof, and the Wave 2 release gate remain.

- Normalize stable provider budget keys before policy evaluation.
- Support closed rule kinds for remaining checkpoints, reset transitions,
  grant availability, and grant-expiry checkpoints.
- Preserve current behavior as the `current` compatibility preset; the first
  observation bootstraps state without emitting events.
- Persist structured evaluations, explanations, typed events, and notification
  intents atomically through the Wave 1 outbox.
- Add read-only `policy validate`, `policy list`, `policy explain`, and outbox
  inspection commands.
- Cut over without dual evaluation or historical replay.

Gate: the compatibility preset matches frozen current alert histories; config
is strictly validated; duplicate concurrent evaluations produce one semantic
event and one outbox row.

Wave 2 release gate: live Codex pacing agrees with current windows, policy
bootstrap emits nothing, subsequent fixture transitions emit exactly the
expected events, and Telegram retries continue through the shared outbox.

## Wave 3: Agent Interfaces, Profiles, and Surface Parity

### 3.1 Shared agent context

- Add one allowlisted `AgentContextService` over read-only cache/store opens.
- Define `scriba.context.v1` and minimized `scriba.event.v1` envelopes with
  per-source age, staleness, availability, and provenance.
- Ship `scriba context --json` first.
- Add an opt-in Unix-socket API with `/v1/context`, `/v1/health`, and durable
  replayable `/v1/events`; loopback TCP requires bearer authentication.
- Add thin stdio MCP exposing the same context service. No refresh/config/send
  mutations are exposed.

Gate: CLI, API, and MCP are semantically identical for one fixture; read-only
queries leave database hashes, mtimes, schemas, and row counts unchanged;
forbidden identifiers never appear.

### 3.2 Multi-account profiles

- Add backward-compatible config schema v2 with stable profile IDs, labels,
  explicit auth paths, enabled state, and a default profile.
- Poll profiles sequentially and isolate health/failure state. One failed
  account cannot stop healthy accounts or global radar evaluation.
- Add profile-aware CLI, server stats/health, Telegram commands, and JSON.

Gate: two independent fixture auth files remain isolated; config v1 still
runs as one implicit profile; no raw auth path or credentials leak.

### 3.3 Delivery adapters and Telegram parity

- Add signed JSON webhooks and a narrow ntfy adapter over the canonical
  notification envelope and outbox.
- Add Telegram account navigation, explicit profile callbacks, stable inline
  navigation, group-user allowlisting, bounded HTML pagination, and robust
  callback handling.
- Keep desktop notifications inside the macOS app; remote desktop delivery is
  ntfy's responsibility.

Gate: adapter retries and terminal/retryable HTTP classes are deterministic;
Telegram content remains below platform limits and all auth paths share tests.

### 3.4 macOS and operational parity

- Add profile selection, grants/profile visibility, truthful stale/degraded
  state, local notification dedupe, and `SMAppService` launch at login.
- Add bounded retention for events/deliveries, scheduled verified backups,
  systemd hardening/linger guidance, reproducible tagged CLI releases,
  checksums, attestations, and security documentation.
- Notarized macOS artifacts require Apple credentials and final bundle
  ownership. Sparkle waits until notarization is stable.

Gate: deterministic Swift previews/tests, packaged helper parity, devbox reboot
and restore drills, clean journal inspection, and release artifact verification.

## Commit and Release Discipline

- One logical change per conventional commit; every commit leaves its focused
  package tests green.
- Commit repo code/spec changes before recording their final hashes in vault.
- Update the vault at every phase gate and release, not only at program end.
- Version bumps, tags, Homebrew updates, deploys, and release receipts remain
  separate commits/steps after product gates pass.
- Never migrate the live database without a verified pre-migration backup and
  a tested previous-binary restore path.

## Kill Criteria

Pause a phase and report a blocker when any of these is true:

- no stable consumer or measurable acceptance test exists;
- the change requires provider-write authority or secret movement outside the
  accepted local model;
- migration cannot preserve or safely classify existing delivery state;
- rollback with the previous binary is unproven;
- a new dependency cannot pass a maintenance/security/size review;
- live validation requires unavailable independent accounts, Apple signing
  credentials, or external infrastructure.

## Known Blockers

- A production-like schema-v6 fixture is required before the outbox migration.
- Multi-account live proof requires two independent Codex auth files. Fixture
  coverage can land first, but the release gate remains blocked without live
  proof unless scope is explicitly revised.
- Absolute OAuth exclusion against the Codex CLI is impossible unless Scriba
  adopts Codex's own lock protocol. Optimistic generation checks remain
  required.
- Off-host backup destination/retention is not defined. Verified local backups
  are recoverability, not disaster recovery.
- Notarized macOS release requires Developer ID/notary credentials and final
  bundle ownership.
- Homebrew updates remain manual until the automated CLI release path has
  proven stable.

## Planning Receipt

Seven independent planning passes covered durable delivery, runtime safety,
budget intelligence, policy semantics, provider contracts, agent interfaces,
and surfaces/operations. A final scope review rejected hardening five delivery
implementations only to replace them later. This roadmap therefore performs
one canonical outbox migration in Wave 1 and makes all later policy/adapters
reuse it.

No product implementation was started before this plan was reconciled.

## Execution Receipts

- 2026-07-12: schema-v7 canonical outbox, durable Telegram inbox, atomic
  five-kind producer enqueue, target-filtered fenced dispatch, and queue
  health/stats activated at `6d4e8e2`. Full local race/lint/security gates
  passed. A verified live-v6 backup containing all five legacy ledgers migrated
  on a disposable copy with exact 38-row reconciliation, clean integrity
  checks, idempotent reopen, schema-v7 backup proof, and previous-binary
  rollback-copy proof. See `docs/schema-v7-migration.md`. Live deployment is
  now verified on devbox at `b999204`: fresh predeploy backup, schema 7,
  healthy empty queues, two clean additional polls, integrity checks, Bot API
  command registration, and a clean journal scan. Interactive `/health`
  durable-inbox smoke remains pending user input.
- 2026-07-12: Wave 1.3 contract laboratory completed through `3aade5a`.
  Codex integers are exact beyond 2^53 with explicit invalid-number rejection;
  model precedence is deterministic; Codex and Claude parser caches have
  semantic namespaces; corpus-seeded fuzz/property tests cover both parsers;
  four existing `scriba.v1` outputs have compiled Draft 2020-12 schemas and
  canonical goldens; GPT-5.6 pricing is loaded from a hash-bound reviewed
  offline catalog with a candidate-only refresh/check workflow; and pinned
  ccusage/openusage projections cover every 271999/272000/272001 GPT-5.6
  boundary. Full Go security/lint gates and all 18 Swift tests passed locally.
