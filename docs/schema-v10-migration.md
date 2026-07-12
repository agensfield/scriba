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

## Live activation receipt

The gate above was completed later on 2026-07-12 after CI run
`29207667932` passed Go race/vet, lint/security, and macOS Go/Swift. OpenAI had
temporarily removed the five-hour limit and moved the lone seven-day bucket
into `primary_window`; commit `9bf7392` added a default-on compatibility flag
without changing normal two-window responses. The kill switch is
`SCRIBA_FEATURE_CODEX_TEMPORARY_NO_FIVE_HOUR=false`. Commit `b790a9a` proves
that a disappearing five-hour window emits no reset or warning and preserves
its last durable state.

The service was stopped before the authoritative schema-v8 backup:

- path: `/home/arda/.local/state/scriba/backups/scriba-server-backup-20260712T202802.201684285Z-e917af8544ee.sqlite`
- SHA-256: `99748dd1756985cc32b77b6c9c72615eb00387c3192e26963930e3c33625673f`
- candidate SHA-256: `288e0c90d29e9231b4cb760076a838aebdd6c46f94f430db934c90dfdf8e5a25`
- preserved business-table data SHA-256: `d9df354fd1581dcdf933af250fd53bf0187290a36872da77cb8abe0fc2ddf26a`
- evidence directory: `/home/arda/.local/state/scriba/cutovers/v10-20260712T202802Z`

The exact stopped-service artifact repeated migration, idempotence, integrity,
unchanged business data, old-binary refusal, and untouched-v8 restore-copy
proof. The candidate was then installed, the owner-only context API was
enabled, and the service migrated live to schema 10.

Two starts and fresh polls proved:

- healthy commit `9bf7392`, schema 10, zero poll failures;
- private `0700` socket parent and owner-only `0600` socket;
- healthy local API plus `scriba.context.v1` and replayable SSE;
- both stdio MCP tools through the official SDK client;
- clean forbidden-field checks across context, SSE, and MCP;
- latest durable primary and Spark windows labeled weekly with seven-day
  durations, while the absent five-hour state retained its last observation;
- exactly one legitimate `primary.weekly` early-reset transition caused by
  OpenAI's announced global reset, with no fabricated five-hour transition;
- two policy events mapped exactly to replay sequences 1 and 2;
- 15 policy states, two policy events, 42 delivered outbox rows, 43 attempts,
  and no pending/dead delivery work;
- `quick_check=ok`, zero foreign-key violations, and a clean service journal.

Wave 3.1 agent transport deployment is complete.
