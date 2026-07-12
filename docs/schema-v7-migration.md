# Schema v7 Migration Receipt

Date: 2026-07-12

Schema v7 activates the canonical notification outbox and durable Telegram
inbox. New notification-eligible business events enqueue their versioned
delivery payload in the same SQLite transaction. The five schema-v6 delivery
tables remain read-only migration evidence.

## Pre-migration evidence

The installed devbox service remained active and healthy on version `0.2.9`,
commit `6b9a85c`, schema 6. Because that installed binary predated the backup
command, commit `0a8e6f1` was cross-built as a temporary backup-only Linux
binary. It opened the state through `OpenExisting`, without migration.

The backup command initially rejected the existing `backups` directory at mode
`0775`. The directory was tightened to `0700`, after which the verified online
backup succeeded:

- size: 33,902,592 bytes
- SHA-256: `ae48b629e9c695b3e4c0b65ff2d5cbbda7ac56f8725b74b6e9d50489ca7196f2`
- schema: 6
- `quick_check`: `ok`
- live source service: active and healthy throughout

The copied backup contained all five legacy delivery ledgers:

| Ledger | Rows |
| --- | ---: |
| reset deliveries | 12 |
| limit-warning deliveries | 12 |
| reset-grant-warning deliveries | 3 |
| reset-grant deliveries | 1 |
| Radar deliveries | 10 |

## Disposable-copy migration

Current commit `6d4e8e2` opened only a local copy of the verified backup.
Migration completed at schema 7 with:

- `quick_check`: `ok`
- `foreign_key_check`: zero rows
- 38 canonical outbox rows, exactly matching the 38 legacy delivery rows
- all five event kinds represented
- every migrated row marked `source=legacy-v6`
- second open remained schema 7 with 38 rows and one schema-version record per
  version, proving idempotence
- a schema-v7 online backup succeeded with schema 7 and `quick_check=ok`

All 38 historical delivery rows were already delivered. Their attempt totals
were preserved: reset 13, limit warning 12, grant warning 3, reset grant 1, and
Radar 10.

## Rollback proof

A fresh disposable copy of the untouched schema-v6 backup was opened on
devbox by the previous `0a8e6f1` binary. It reported schema 6 successfully.
The immutable remote backup hash still matched the original receipt afterward.

No current binary has opened the live database yet. Live deployment requires a
fresh predeploy backup, service stop/install/start, schema-7 health/stats proof,
Telegram command smoke, two clean poll cycles, and a journal scan for SQLite
lock errors or duplicate delivery.
