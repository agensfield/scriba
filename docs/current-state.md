# Scriba Current State

Date: 2026-09-10

Scriba is a local-first Claude Code and Codex usage tracker. The maintained
product is the Go CLI, resident server, Telegram bot, owner-only Unix HTTP/SSE
API, and stdio MCP server. The macOS app remains archived under `apps/macos`
and is outside the active roadmap.

## Release status

`v0.4.0` is published and deployed on devbox at
`5dc56405896be7582aac7b73489c038fa5c67d85`, using database schema 13.
Exact-head CI, independent reviews, reproducible publication, checksums and
attestations, public Linux Homebrew upgrade, stopped-service migration, and two
normal post-cutover polls passed. Both accounts and their history are intact;
`personal` is readable without credentials and `antari` is current. Full
evidence is preserved in [`release-v0.4.0.md`](release-v0.4.0.md) and
[`schema-v13-migration.md`](schema-v13-migration.md).

## Current product

### Accounts and auth sources

- Stable public account IDs are derived from strong provider account identity.
  Raw provider refs, auth paths, and credentials stay private.
- Config v3 contains ordered `codexAuthPaths`, not user-facing profiles.
  Omitted paths use standard Codex discovery; explicitly configured missing
  paths never fall back elsewhere.
- Valid local auth identities are discovered automatically. A source switching
  from account A to B creates or reactivates B without renaming, deleting, or
  transferring A's history.
- `scriba accounts` lists active and historical accounts. Credential
  availability, last successful observation, age, and staleness are distinct
  facts. Optional aliases are unique metadata and never change Codex login.
- `--account <id-or-alias>` selects Codex limits, grants, budgets, activity,
  reset, status, and context. Omission follows the highest-priority usable auth
  source for live work. Explicit unknown selectors fail closed.
- Stored historical limits, grants, and budgets remain readable without
  credentials. Live-only activity and reset require credentials for that exact
  account.

### Resident state and safety

- Schema 13 replaces live profile ownership with auth-source bindings and
  account alias metadata. Migration preserves observations, policy state and
  events, replay ordinals and prune floors, outbox IDs/payloads/outcomes, and
  archived source-health evidence. See
  [`schema-v13-migration.md`](schema-v13-migration.md) for root-owned rehearsal
  and deployment evidence.
- Each resident source is inspected and polled independently. Missing,
  malformed, logged-out, API-key, and rotated credentials cannot silently
  borrow another source's account.
- Limits/activity/reset preview requests pin the selected provider identity
  before network access and after forced OAuth refresh. Reset confirmation also
  pins the public account, private preview identity, selected credit, user/chat,
  expiry, and idempotency key. An auth switch refuses redemption.
- Account observations, policy state/events, warnings, and outbox rows remain
  account-scoped. Radar alerts remain global and accountless.
- Retention preserves pending/leased work, monotonic replay high-water state,
  and one explicit prune floor per account. Backups use verified online
  snapshots; schema cutovers and restores require a stopped service.

### Public surfaces

- CLI account commands are `scriba accounts [list]` and
  `scriba accounts alias <id-or-alias> <new-alias>`.
- The upstream ChatGPT/Codex activity command is `scriba codex activity`.
- `scriba server accounts` exposes account summaries; server health separates
  source health from historical account freshness.
- Telegram uses `/accounts`, `/limits [account]`, `/grants [account]`,
  `/activity [account]`, and `/reset [account]`. Callbacks contain only public
  account IDs; old controls fail safely rather than retaining another model.
- Agent context is `scriba.context.v2`; event pages and records are
  `scriba.events.v2` and `scriba.event.v2`. HTTP and MCP selectors are named
  `account`. SSE streams pin one resolved public account for their lifetime.
- External delivery uses `scriba.notification.v2` with `accountId` derived from
  the durable outbox account. Possibly email-derived labels are omitted; Radar
  remains accountless.

### Local usage and pricing

- Claude and Codex reports preserve effective tokens separately from total
  model traffic, exact integer counters, timezone-aware grouping, and all
  materially used model names.
- Local Codex JSONL totals are scoped to session logs. They cannot be split
  across provider accounts from auth-switch intervals; selected-account reports
  state that attribution is unavailable.
- Known models receive an as-of Standard-tier API-equivalent estimate, not a
  ChatGPT subscription charge or historical invoice. The embedded reviewed
  catalog uses current GPT-5.6 Sol/Terra/Luna pricing and invalidates parsed
  caches when catalog content changes. Frozen third-party differential receipts
  retain their capture-time rates.
- `status --fast`, `codex limits --fast`, and `codex reset-grants --fast` use
  the selected account's resident observation without network access. They do
  not reuse anonymous or cross-account Codex quota.

## Operational invariants

- Source logs and provider APIs remain authoritative; cache deletion is safe.
- Ordinary local scans stream JSONL and never infer account ownership that the
  source cannot prove.
- Read-only account/context/MCP/fast paths do not refresh OAuth, sync sources,
  migrate schemas, or mutate state.
- The server database, config, auth-source directories, backup directory,
  environment file, and Unix socket remain owner-only.
- `--redact` removes human-identifying and operator-private fields appropriate
  to each JSON surface.
- The shipped context API is Unix-socket-only. No TCP listener is enabled.

## Evidence boundary

The release receipt distinguishes synthetic tests, copied-production drills,
public artifact verification, and actual devbox activation. Linux Homebrew
installation was tested; macOS Go/Swift and cross-built archives passed CI, but
no macOS Homebrew installation test is claimed. No live reset grant was
redeemed during verification. The old binary/config and authoritative schema-12
backup remain available for restore-only rollback.

Older release, schema, and control-plane documents remain immutable historical
evidence. Their profile terminology describes the product that existed at that
time and is not current usage guidance. The
[`control-plane-roadmap.md`](control-plane-roadmap.md) is likewise a historical
program record; use this document, [`cli.md`](cli.md), and
[`config.md`](config.md) for current usage.
