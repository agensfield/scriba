# Schema v13 Account Migration

Status: copied-production migration and old-binary rollback drill passed;
production activation pending.

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

### Live activation remains pending

These copy-only drills do not constitute a live deployment. Before activation,
take an authoritative stopped-service backup, retain
the old binary/configuration, and verify the released candidate. After
activation, verify both retained accounts, current credential bindings,
account-specific views, aliases, queues, and normal resident polling. An older
binary must use the pre-migration backup; do not downgrade a v13 database in
place.

Raw databases remain owner-only devbox artifacts under Scriba's state directory;
they do not belong in the repository or Vault.
