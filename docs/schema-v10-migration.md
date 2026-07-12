# Schema v10 Migration Rehearsal

Date: 2026-07-12

Schema v9 assigns every policy event a transactional monotonic replay sequence.
Schema v10 adds replay tombstones and durable high-water state so retention
cannot silently create cursor holes. This receipt covers a disposable copy of a
fresh live schema-v8 backup. It does not authorize or claim live activation.

## Verified source backup

The installed `e3cd9b2` binary created an online verified backup while the live
service remained active:

- path: `/home/arda/.local/state/scriba/backups/scriba-server-backup-20260712T115600.289938884Z-e4c00f288240.sqlite`
- SHA-256: `a2ff6d613bec7eb612b2f4254479a8ce86ad7a870a405a96c176e2138a79b860`
- schema: 8
- size: 34,525,184 bytes
- `quick_check`: `ok`
- evidence directory: `/home/arda/.local/state/scriba/cutovers/v10-20260712T115600Z`

The source contained 15 policy states, one policy event, 41 canonical outbox
rows, and 12 legacy notification-delivery rows.

## Disposable-copy proof

The static Linux amd64 candidate embedded commit `0d97c27` and had SHA-256
`ee915d7e85e2354daba2e542d7bdb6d77d128f641230b68a56cf0642eecf9614`.
It opened only a mode-0600 disposable copy of the verified backup.

The rehearsal proved:

- the first open applied schema versions 9 and 10 once each;
- the second open was idempotent;
- `quick_check` returned `ok` and `foreign_key_check` returned no rows;
- a data-only dump of every pre-existing business table had identical SHA-256
  `86ce74c5d3839a8549e95be5f53288c3576160bc3692efb59a71d6e93375c99c`
  before and after migration;
- all 15 policy states, the policy event, 41 outbox rows, and 12 legacy
  notification-delivery rows were unchanged;
- the existing policy event mapped to replay sequence 1 and the durable
  `policy_event_replay` high-water was 1;
- the preserved `e3cd9b2` binary refused the migrated copy with
  `database schema version 10 is newer than supported version 8`;
- a fresh untouched restore copy retained the source SHA-256 and opened
  successfully with `e3cd9b2` at schema 8.

The live service stayed active and its database remained schema 8 throughout.

## Remaining live gate

Before live activation, stop the service and create a new authoritative
schema-v8 backup. Repeat the candidate migration and previous-binary
restore-copy checks against that exact stopped-service artifact. Only then
install the candidate, enable the owner-only context API, restart, and run
health, context, SSE reconnect, MCP, privacy, read-only, journal, and second
restart smokes.
