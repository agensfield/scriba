# Control-Plane Completion Audit

Date: 2026-07-13

This audit compares the accepted requirements in
[`control-plane-roadmap.md`](control-plane-roadmap.md) with current source,
tests, committed migration/deployment receipts, the public release, and live
devbox state. A status paragraph by itself is not treated as proof.

## Current authoritative state

- The published implementation/release baseline at `c085261` has exact-HEAD
  green CI `29260760662`.
- Audit follow-up `a45d7c3` adds the missing contract/property tests, PR archive
  packaging smoke, and explicit conditional TCP scope. Local `just check`,
  `go test -race -count=1 ./...`, all archive checksums, and the host archive
  executable smoke pass. Exact-HEAD CI `29261669059` is green, including the
  new Linux archive/executable smoke.
- `v0.3.0` resolves to `92e7147`, is stable and immutable, and contains exactly
  the four expected platform archives plus `checksums.txt`.
- Release workflow `29259749842` passed source race/vet, independent
  deterministic builds, byte equality, checksums, Linux smoke, attestation
  creation/verification, draft asset verification, and publication.
- Independent consumer checks verified every checksum and all five
  attestations. The pinned installer and Homebrew `0.3.0` fetch passed.
- Devbox runs the exact public Linux amd64 binary, version `0.3.0`, commit
  `92e7147`, schema 11. Service and backup timer are active/enabled; default
  profile is healthy; active/dead queues are empty.
- The current source passed local `just check`; the release source passed
  `go test -race -p 1 ./...` and vet. Current CI separately passed parallel Go
  race/vet, macOS Go/Swift, and lint/security.

## Requirement matrix

| Requirement | Verdict | Authoritative evidence |
| --- | --- | --- |
| Phase 0 planning/evidence | Proven | Accepted dependency order and planning receipt in the roadmap; repo/vault history from `0c1ac32`; an owner-only production-like schema-v6 copy drove the migration tests and no database containing identifiers was committed. |
| Wave 1.0 CI/recoverability | Proven | Audit follow-up `a45d7c3` adds CLI/server archive packaging, checksums, and executable smoke to PR CI; exact-HEAD CI `29261669059` passed it. Backup implementation/tests, live-WAL and restore receipts in `schema-v7-migration.md`, and current scheduled-backup/restore proof in `operations.md` remain proven. |
| Wave 1.1 SQLite/OAuth/network/process safety | Proven | Connection, OAuth, ingress, timeout, health, supervision, shutdown, and race tests remain in the current suite; live service restart/reboot receipts are committed. |
| Wave 1.2 canonical durable outbox | Proven | Schema-v7 copied-live migration/rollback receipt; producer atomicity, fenced claim, retry/dead-letter, legacy preservation, queue health, and real Telegram retry tests pass in current source. |
| Wave 1.3 provider contract laboratory | Proven | The frozen corpus now includes generated oversized-line policy and synthetic markers matching current Codex fork/replayed-history rollout shapes in addition to malformed/boundary cases. Exact-number decoders, semantic cache versions, JSON schemas, fuzz/property/golden tests, offline differential checks, and hash-bound pricing refresh/check workflows pass exact-HEAD CI. |
| Wave 1 release gate | Proven | Copied production migration, backup/restore, devbox deployment, durable Telegram delivery/ingress, two-poll soak, SQLite integrity, and current full gates are recorded and remain compatible with schema 11. Human health update `133548446` (`quick:health`) was durably processed on 2026-07-13. |
| Wave 2.1 deterministic pacing | Proven | Provider-neutral pure budget package, fixed-clock/property/fuzz/render tests, bounded history, typed schemas, both provider commands, and explicit no-token-influence tests. `a45d7c3` adds the missing monotonic projection property over increasing utilization; exact-HEAD CI passes. |
| Wave 2.2 typed policy runtime | Proven | Schema-v8 migration/deployment receipt; all four closed rule kinds; atomic state/event/outbox tests; silent bootstrap, compatibility history, duplicate-concurrency, inspection surfaces, and real retry integration proof. |
| Wave 2 release gate | Proven | `budget-and-policy.md`, schema-v8 live receipt, and composed `6bfbcb3` fixture/SQLite/Telegram evidence cover bootstrap, exact transitions, payload parity, and retry completion. A fresh 2026-07-13 live comparison matched the quota source at weekly used 27%, remaining 73%, reset `2026-07-19T20:15:45Z`; the derived budget reported the same inputs and deterministic high-risk pacing. Spark likewise matched at 0% used. |
| Wave 3.1 shared agent context | Proven | Schema-v10 migration/rollback; direct/CLI/Unix HTTP/SSE/MCP parity tests; privacy and semantic read-only checks; live API/SSE/MCP activation receipt; healthy v0.3.0 CLI/API/SSE smoke. The roadmap now states explicitly that TCP is a conditional future constraint, not a program deliverable. |
| Wave 3.2 profile implementation | Proven | Config-v1 compatibility, config-v2 isolation, schema-v11 migration/rollback, rotation, isolated polling/health, public surface parity, strict selectors, privacy tests, and composed two-auth fixture proof. |
| Wave 3.2 revised live gate | Proven | On 2026-07-13 Arda explicitly accepted composed two-fixture isolation plus one-account live compatibility because no second authorized account is available. Fixtures prove successful multi-account routing/mapping/privacy; the devbox proves compatibility-profile deployment and public-surface parity. A distinct unavailable backup account additionally failed closed without affecting the healthy profile or modifying either source file. |
| Wave 3.3 webhook/ntfy/Telegram parity | Proven | Signed canonical webhook and ntfy delivery, deterministic HTTP outcomes, retry/fencing, Telegram pagination/navigation/auth/callback tests, deployment, command registration, and healthy queues are proven. Human updates `133548448` (`quick:profiles`), `133548449` (`profiles:v1:open:default`), and `133548450` (`profiles:v1:limits:default`) were durably processed on 2026-07-13. |
| Wave 3.4 operations/release parity | Proven | Retention invariants/tests; scheduled verified backups; hardened user units and linger; stopped-service restore and autonomous reboot; security policy; immutable reproducible v0.3.0, checksums/attestations, Homebrew, and exact-artifact deployment. See `release-v0.3.0.md`. |
| macOS menu app | Excluded | Arda explicitly removed menu-app development from this program. Existing Swift code and CI are preserved, but no new UI/package parity is a completion requirement. |

## Accepted boundaries, not hidden completion claims

- Optimistic generation checks protect Scriba OAuth writes. Absolute exclusion
  against a non-cooperating Codex CLI process is impossible without sharing
  Codex's lock protocol and was never promised.
- Local verified backups provide recoverability, not disaster recovery. No
  independently controlled off-host destination or retention policy has been
  selected.
- The temporary OpenAI weekly-only primary-window shape remains behind its
  default-on, kill-switchable feature flag and preserves disappearance-safe
  five-hour state/reset semantics.

## Sequence deviation

The accepted roadmap said not to begin a later wave before the prior wave's
release gate passed. Wave 3.3, Wave 3.4, and `v0.3.0` nevertheless shipped
while the then-binding Wave 3.2 two-real-account live gate remained open. The
gate was subsequently revised explicitly, so no product proof remains missing,
but the historical sequencing was still a process-contract violation.

## Verdict

The full control-plane goal is **complete** under the explicitly revised Wave
3.2 contract. All requirement rows are proven or explicitly excluded. The
human Telegram health and profile-navigation receipts are processed durably,
the live inbox is 10 processed with zero pending or dead work, and the absence
of a second authorized Codex account is no longer a release gate.
