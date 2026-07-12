# Schema v8 Migration Receipt

Date: 2026-07-12

Schema v8 adds durable policy state and semantic policy events. Commit
`c32d885` also makes the closed `current` policy preset the resident server's
single evaluation path, with first-evaluation bootstrap and atomic persistence
through the schema-v7 canonical outbox.

## Pre-migration backup

The service was stopped before the authoritative schema-v7 backup was taken:

- path: `/home/arda/.local/state/scriba/backups/scriba-server-backup-20260712T060027.864426917Z-594e3cdae19e.sqlite`
- SHA-256: `e92458e196c0ad163538b93989f835f80b6f46f605f266b101c1d7f0a5547703`
- evidence directory: `/home/arda/.local/state/scriba/cutovers/v8-20260712T060027Z`

This retained, checksum-verified backup is the rollback artifact. Schema downgrade remains
restore-only: an older binary must open a restored pre-v8 copy, never the live
v8 database.

## Disposable-copy proof

A copy of the stopped-service backup passed the complete v8 rehearsal:

- migration reached schema 8 exactly once;
- a second open was idempotent;
- `quick_check` returned `ok`;
- `foreign_key_check` returned zero rows;
- existing business-event identities were preserved, while aggregate
  notification outbox row and attempt counts remained unchanged;
- the previous `b999204` binary successfully opened a separate rollback copy
  restored from the untouched schema-v7 backup.

## Live deployment receipt

Commit `c32d885` was installed on the devbox and migrated the live database to
schema 8 exactly once. Migration and reopen left policy tables empty. The
first fresh post-cutover refresh separated account bootstrap from policy
bootstrap and produced:

- 15 `policy_states` rows;
- zero `policy_events` rows, proving bootstrap emitted no historical events;
- 40 delivered canonical outbox rows and 41 total attempts, unchanged across
  migration;
- zero pending, leased, expired, or dead-letter outbox rows and no Telegram
  inbox failure queue;
- two explicit post-cutover refreshes with clean policy evaluation and no
  duplicate or spurious events;
- live `quick_check=ok` and zero `foreign_key_check` rows.

The expected direct Telegram send at startup was traced to the configured
startup heartbeat and its `startup_heartbeat_at` state, not policy delivery.
The Telegram Bot API returned `true`, and all 11 commands were registered.

The schema-v8 policy runtime cutover is therefore complete. Read-only policy
`validate`, `list`, `explain`, and outbox inspection CLI surfaces remain future
work and are not implied by this receipt.
