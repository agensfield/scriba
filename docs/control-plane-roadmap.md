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

Status: live runtime cutover completed at commit `c32d885`. Schema v8, atomic
policy persistence/event production, bootstrap without replay, copied-live
migration proof, and the devbox deployment receipt are complete. Read-only
policy validation/list/explanation and outbox-list inspection surfaces are also
deployed at commit `a168e34`, with typed JSON schemas and read-only store
access. The Wave 2 release gate was closed by the integration proof in
`6bfbcb3`.

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
Passed at `6bfbcb3`: the composed fixture covers all four policy kinds and
policy/outbox identity and payload parity; a real temporary SQLite outbox and
Telegram SDK test proves failure, backoff, reclaim, and delivered completion.

Current gate status: passed. Live pacing, silent bootstrap, runtime cutover,
read-only inspection, exact subsequent fixture transitions, and Telegram
retry-through-outbox behavior are proven.

## Wave 3: Agent Interfaces, Profiles, and Surface Parity

### 3.1 Shared agent context

Status: CLI slice completed and deployed at `e3cd9b2`. The allowlisted
`AgentContextService`, `scriba.context.v1` and minimized `scriba.event.v1`
contracts, and JSON-only `scriba context --json` are live. The deployment smoke
returned eight independently described sources, Codex on the `default` profile,
zero events, and unavailable/missing Claude data without failing the envelope.
Repeated reads preserved database hashes and schema/policy/outbox counts, and
the privacy-forbidden grep was clean. Full Go race/check gates and all 18 Swift
tests passed.

Durable replay sequencing began at `6a6a163` as schema v9. Commit `8b6272e`
adds the public `scriba.events.v1` paging contract and schema v10 tombstones so
retention cannot silently create replay holes. Versioned fixed-width cursors,
captured high-water bounds, expiry errors, poison-row bounds, and account-pinned
pages are covered. The hardened Unix-listener lifecycle primitive landed at
`fc663de`. Commits `29fd69a`, `e157b66`, `9051ce9`, and `249991d` implement the
owner-only Unix HTTP/SSE API, stdio MCP tools, response contracts, and CLI/server
wiring. Commit `e659570` closes the real stdio lifecycle proof. A shared fixture
proves context parity across direct service, CLI, Unix HTTP, and MCP, plus event
and cursor parity across direct service, SSE, and MCP. None of the schema-v9/v10
or agent-transport changes are deployed yet; timestamp or hashed-event-ID
cursors remain forbidden.

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

Current gate status: passed. The authoritative stopped-service schema-v8 backup
repeated the disposable v10 migration/rollback proof before live activation.
The deployed owner-only Unix API, SSE replay, and stdio MCP passed cross-surface,
privacy, read-only, restart, and journal smokes at `9bf7392`. OpenAI's temporary
weekly-only primary shape is isolated behind a default-on kill-switchable flag,
and `b790a9a` proves five-hour disappearance emits nothing while preserving its
last state. See [`schema-v10-migration.md`](schema-v10-migration.md).

### 3.2 Multi-account profiles

- Use server schema v11 for profile/account mappings and isolated poll health;
  schemas v9 and v10 are the earlier replay-sequence and tombstone migrations.
- Config v2 owns stable bounded profile IDs, labels, enabled state, one enabled
  default, and explicit absolute disjoint Codex auth paths. It never consults
  ambient `CODEX_HOME`. Config v1 still loads as one in-memory implicit
  `default` profile using its existing account label and auth discovery, without
  rewriting the file.
- Profile identity comes from config; provider account identity is observed.
  Schema v11 stores safe profile metadata, historical/current account mappings,
  and sanitized per-profile poll health. It never stores auth paths. A newly
  observed mapping is committed atomically with its account poll, policy events,
  and outbox intents; one provider account cannot silently belong to two
  profiles.
- Poll enabled profiles sequentially in config order with bounded per-profile
  work. One failed account cannot stop healthy accounts, global Radar
  evaluation, or global pruning. Aggregate health is derived from isolated
  profile health; backoff applies only when every enabled profile fails.
- Add safe profile-aware CLI and typed JSON, server refresh/stats/health,
  agent-context selection across CLI/Unix HTTP/SSE/MCP, and minimal Telegram
  `/profiles` plus profile arguments. Full Telegram inline navigation remains
  in 3.3.

Gate: two independent fixture auth files remain isolated; config v1 still
runs as one implicit profile; v10-to-v11 migration/rollback and account
rotation are proven; no raw auth path, account ref, auth source, or credentials
leak. Live release proof still needs two independent real Codex auth files.

Current gate status: implementation, composed two-fixture isolation, schema-v11
migration/rollback, compatibility-profile deployment, and all public-surface
smokes passed through `538d557`. The devbox has only one real Codex auth file,
so the explicitly separate two-real-account live proof remains externally
gated. See [`schema-v11-migration.md`](schema-v11-migration.md).

### 3.3 Delivery adapters and Telegram parity

- Add signed JSON webhooks and a narrow ntfy adapter over the canonical
  notification envelope and outbox.
- Add Telegram account navigation, explicit profile callbacks, stable inline
  navigation, group-user allowlisting, bounded HTML pagination, and robust
  callback handling.
- Remote desktop delivery is ntfy's responsibility.

Gate: adapter retries and terminal/retryable HTTP classes are deterministic;
Telegram content remains below platform limits and all auth paths share tests.

Current adapter status: passed through `fe60414`. All producers atomically fan
out to stable target IDs. Webhook and ntfy share a minimized bounded envelope;
exact-body HMAC, redirect refusal, deterministic HTTP outcomes, capped
`Retry-After`, shutdown fencing, continuous backlog drain, env-only secrets,
and real SQLite-to-both-adapters parity are proven. Telegram parity passed
through `68814b1`: versioned profile pages/actions, bounded callback/HTML
surfaces, explicit group-user allowlisting, inaccessible-message authorization,
stale-control retirement, closed logging, and durable API-failure propagation.
Wave 3.3 is deployed at `ce03c7a`; health, schema-11 integrity, queues, command
registration, and journal smokes passed. One human inline profile-button tap
remains the interactive callback proof.

### 3.4 Operational and release parity

- Add bounded retention for events/deliveries, scheduled verified backups,
  systemd hardening/linger guidance, reproducible tagged CLI releases,
  checksums, attestations, and security documentation.

Gate: devbox reboot and restore drills, clean journal inspection, and
reproducible CLI/server release artifact verification.

Operational status through `eb74648`: bounded daily retention preserves
pending/leased work, exact timestamp boundaries, global replay high-water, and
one explicit per-account prune floor. The devbox runs repo-identical hardened
user units with linger enabled and a persistent daily verified-backup timer.
An actual stopped-service backup boot and exact live-state restoration both
passed schema/integrity/queue health, followed by an autonomous reboot with
zero failed user units or journal error hits. Reproducible tagged artifacts,
checksums, attestations, security policy, and release evidence remain.

The existing macOS menu app is preserved but explicitly outside this program.
No menu-app UI parity, packaging, launch-at-login, notarization, or Sparkle work
is required for completion.

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
- live validation requires unavailable independent accounts or external
  infrastructure.

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

Wave 3.2 was re-reconciled on 2026-07-12 after Wave 3.1 deployment. The binding
sequence is config-v2 compatibility and explicit auth routing, schema-v11
durable identity, isolated sequential polling, typed CLI/server JSON, agent
transport selection, minimal Telegram profile arguments, then composed
two-auth isolation proof. Stable profile identity must come from durable
mappings rather than being painted onto account-scoped history. The same scope
review removed all macOS menu-app development from the program while retaining
CLI/server operational and release work.

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
- 2026-07-12: Wave 2.2 schema-v8 policy runtime deployed at `c32d885`.
  A stopped-service schema-v7 backup with SHA-256 `e92458e196c0ad163538b93989f835f80b6f46f605f266b101c1d7f0a5547703`
  passed copied-live migration, idempotence, integrity, business-event identity
  and aggregate outbox-count preservation, and `b999204` rollback-copy proof.
  Devbox is healthy on schema
  8 with 15 policy states, zero bootstrap events, 40 delivered outbox rows and
  41 preserved attempts, two clean explicit refreshes, and all 11 Telegram
  commands registered. See `docs/schema-v8-migration.md`.
- 2026-07-13: Wave 3.3 delivery adapters completed through `fe60414`. Commit
  `47d3455` widens every producer to atomic multi-target fanout; `533183c`
  freezes the target-independent `scriba.notification.v1` body and HTTP
  adapters. The final dispatcher adds env-only config, exact-body HMAC, ntfy
  JSON publishing, deterministic terminal/retryable classes, capped
  `Retry-After`, fenced shutdown completion, continuous backlog drain, and a
  real SQLite webhook/ntfy parity test. Full local race/lint/security gates and
  independent adversarial review passed.
- 2026-07-13: Wave 3.3 Telegram parity completed at `68814b1`. Versioned
  six-profile pages and exact profile actions stay within Telegram's byte/text
  limits. Empty allowlists are private-chat-only; explicit group users require
  both chat and user matches. Stale/disabled profiles cannot retain active
  controls or fall back. Chat-backed inaccessible messages remain authorized,
  unknown callback logs are closed, and callback API failures propagate to the
  durable inbox. Two adversarial review rounds and full local gates passed.
- 2026-07-13: Wave 3.3 deployed at `ce03c7a` after CI run `29215414130`
  passed every job. Linux amd64 SHA-256 is
  `e4387d92f4525a470a64d9386c73d871071e966d896291037ad25c8e88371b1e`.
  Live schema 11 remained healthy with zero pending/leased/dead inbox or outbox
  work, all 12 Telegram commands registered, `quick_check=ok`, and a clean
  journal. The previous `538d557` binary is preserved in the deployment
  evidence directory. The macOS SSE CI flake was fixed at `ce03c7a` by waiting
  for stream-slot release; the focused test passed 100 normal and 20 race runs.
- 2026-07-13: Wave 3.4 retention and operations passed through `eb74648`.
  Retention review caught and fixed global-sequence/account-floor expiry,
  variable-width timestamp comparison, unbounded-duration overflow, and
  write-lock amplification. Focused race/security gates passed. The live
  `922d464` Linux amd64 binary SHA-256 is
  `393fbf666015806aedd35da6e8d82133b37654fe4e5304355bf13cdfcb15f75d`.
  The first hardening activation failed closed on unsupported user-manager
  capability changes and automatically rolled back; the corrected repo-identical
  units then passed. A 35,532,800-byte schema-11 restore candidate with SHA-256
  `f67582dfde9fb1dfe541372ad4ede8a5e5e90558380be6263a916b3a0febca01`
  booted healthy with `quick_check=ok`, zero FK violations, 44 delivered outbox
  rows, and three processed inbox rows; the exact pre-drill state was restored
  and reverified. After reboot, service/timer were active and enabled, linger
  was `yes`, queues were empty, no user units failed, and the boot journal had
  zero failure-pattern hits. Evidence lives at
  `~/.local/state/scriba/deployments/wave34-922d464` on devbox.
