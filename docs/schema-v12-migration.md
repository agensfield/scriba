# Schema v12 Migration and Activation

Date: 2026-07-14

Schema v12 adds reset-scoped pacing-warning state and immutable typed pacing
events. This receipt covers the stopped-service schema-11 source, disposable
migration and rollback proof, exact-release activation, and notification
deduplication smoke.

## Authoritative source and release

- source backup:
  `/home/arda/.local/state/scriba/backups/scriba-server-backup-20260713T210857.277652087Z-123fec01cd03.sqlite`
- source SHA-256:
  `8bddd0900ccb8c15af8be4a99e3b61fe1a7a4bccfcce10fdae4e20fe2ceafae7`
- source schema: 11
- release: `v0.3.1`
- release commit: `9276bd3d81477237704266043504e5cf4bea497b`
- Linux amd64 archive SHA-256:
  `c6f57c9a345c0888d50a1321be658876a79bbdd14780d08a084590c54c377d3e`
- installed binary SHA-256:
  `71d2cd70a4c01c1155392b6462b82752999b1837e8c2735273db98d2738ca02c`
- evidence directory:
  `/home/arda/.local/state/scriba/deployments/release-v0.3.1-9276bd3-20260713T210828Z`

The schema-11 binary created and verified the backup while the service was
stopped. The source reported `quick_check=ok`, schema 11, 36,167,680 bytes,
and no pruning.

## Migration and rollback proof

Two independent copies of the stopped-service backup were used. The exact
release binary migrated one copy to schema 12, then opened it a second time
without another migration. SQLite returned `quick_check=ok`, no foreign-key
violations, schema 12, and empty new pacing tables before activation.

The previous `v0.3.0` binary refused the migrated schema-12 copy and opened the
untouched schema-11 rollback copy successfully. That rollback copy retained
`quick_check=ok`, no foreign-key violations, and schema 11. The old service was
restarted after rehearsal and remained healthy before live activation.

Downgrade remains restore-only. Preserve the failed/new database, restore the
verified schema-11 backup with the service stopped, install the previous
binary, and only then restart. Never point the previous binary at schema 12.

## Live activation and anti-spam proof

The exact public Linux binary migrated live state to schema 12. The first poll
observed the real weekly window at high pacing risk with more than 20 percent
remaining. It created one `pacing_warning_events` row and one canonical outbox
row; Telegram delivered that row once.

An explicit refresh and two subsequent service restarts left all three counts
at exactly one: semantic event, outbox intent, and delivered notification. The
final service is healthy on `v0.3.1`/`9276bd3`, SQLite integrity and foreign
keys are clean, the service and backup timer are active, and inbox/outbox state
has zero pending, leased, due, expired, or dead work. The post-activation
journal has no panic, fatal, migration, poll, notification, or dead-letter
failure pattern.
