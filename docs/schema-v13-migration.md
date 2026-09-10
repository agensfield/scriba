# Schema v13 Account Migration

Status: copied-production migration and old-binary rollback drill passed;
schema 13 activated on devbox with v0.4.0 on 2026-09-10.

Schema v13 makes provider accounts the durable identity and records credential
sources separately. It adds optional account aliases and first-seen metadata,
replaces configured-profile ownership with source bindings and fenced source
poll health, and attributes the canonical outbox directly to accounts.
Legacy poll-health rows remain isolated migration evidence. Account history,
policy decisions, event payloads, replay ordinals, and delivery identities are
preserved.

## Verified rehearsal

The rehearsal used the old resident binary's verified online backup:

- Created: `2026-09-10T20:07:36.163279876Z` on devbox.
- File: `scriba-server-backup-20260910T200736.163279876Z-191e498bd09a.sqlite`.
- Size: 68,136,960 bytes; schema 12; `quick_check=ok`.
- SHA-256: `37d232cbea5865802613f3d04182b682c434064cd517e84dd7efcb032c0c9a2a`.
- Candidate store checkpoint: `e03bf3d` (implementation `7a446f2`, reviewed
  follow-up `492c71e`).

`TestCopiedSchema12To13` opened that backup read-only and migrated a second,
test-owned copy. It closed and reopened the migrated database and compared
stable projections of 24 business/state tables, excluding only the deliberately
removed outbox `profile_ref` column and newly added account metadata.

| Preserved data | Rows | Result |
| --- | ---: | --- |
| Accounts | 2 | Hash matched |
| Limit observations | 27,387 | Hash matched |
| Observed windows | 83,167 | Hash matched |
| Policy states | 59 | Hash matched |
| Policy events and replay rows | 65 each | Both hashes matched |
| Canonical outbox | 132 | IDs, payloads, states, attempts, and receipts matched |
| Telegram updates | 199 | Hash matched |

All other checked reset, warning, grant, pacing, radar, legacy delivery, and
server-setting tables also matched. The reopened copy reported schema 13,
`integrity_check=ok`, and zero foreign-key violations. The source backup's hash
remained unchanged after the rehearsal.

The synthetic migration tests additionally exercise atomic failure rollback,
two historical accounts without inherited shared aliases, immutable outbox and
replay identity, malformed-schema refusal, and repeated opening. Source health
tests preserve attempt fencing, abort compensation, alert-state CAS, and
cross-source isolation. Account queries use one read snapshot and are tested
with a one-connection pool and concurrent readers.

## Activation boundary

### Actual old-binary compatibility drill

At `2026-09-10T21:14:36Z`, an independent verifier rebuilt the candidate from
`269e777` and migrated a disposable copy through one isolated alias metadata
write. A schema-2 synthetic config supported both binaries while naming only a
nonexistent auth source and disabling local providers, resident service,
context API, Telegram, and external delivery.

The actual installed 0.3.4 binary refused the schema-13 copy with
`database schema version 13 is newer than supported version 12` and exit 1.
It successfully ran read-only outbox inspection against an untouched schema-12
rollback copy. That rollback copy and the source backup retained the original
SHA-256 above. Both copies passed integrity and foreign-key checks.

The cross-version business-data projection hash remained
`6178243611d2cfc1cbddc210add3239fb8238d0be89a82b2444982be75d8f0ca`
before migration, after migration, and after old-binary refusal. The only
intentional business-metadata change was the disposable alias. An initial
attempt with config schema 3 was rejected before database access; the final
proof used schema 2 and reached the database compatibility guard.

### Devbox activation

The authoritative stopped-service schema-12 backup was created at
`2026-09-10T21:55:38.199340750Z`:
`scriba-server-backup-20260910T215538.199340750Z-c01ccc09d6b8.sqlite`,
68,202,496 bytes, SHA-256
`646ac84aca19a882adf7a7576a7d4588523f37e30b9a5d9eb76f54ae44a3c869`.
The final copied-production test preserved every compared projection from that
backup: 27,408 observations, 83,230 observed windows, 65 policy events/replay
rows, 132 delivered outbox rows, 201 Telegram updates, and both accounts.

The verified public v0.4.0 artifact at `5dc5640` was activated at
`2026-09-10T21:56:11Z`. The live database opened at schema 13 with integrity OK
and zero foreign-key violations. Startup polling added one correctly owned
Antari observation without changing the 65 policy events or 132 delivered
outbox rows. Personal history remained readable without credentials. Both
accounts were explicitly named after verifying their identities; source auth
and the byte-identical v1 configuration were not replaced.

The old binary, original configuration, authoritative backup, and private
deployment receipts are preserved under
`~/.local/state/scriba/deployments/release-v0.4.0.o1RYlb`. An older binary must
use that pre-migration backup; do not downgrade a v13 database in place.
Stop the service and preserve the failed database and all SQLite sidecars
before restoring. See [release-v0.4.0.md](release-v0.4.0.md) for the complete
post-cutover verification receipt.

Raw databases remain owner-only devbox artifacts under Scriba's state directory;
they do not belong in the repository or Vault.
