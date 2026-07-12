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

The schema-v8 policy runtime cutover is therefore complete.

## Policy inspection deployment receipt

Commit `a168e34` was installed on the devbox after the migration cutover. The
installed binary SHA-256 was
`f464fe73c89a4eda3fa990c5bc3905880199cd4a774324eaca897d31395e8741`.
After restart, `scriba server health --env prod --json` reported the same
commit, `status=ok`, zero consecutive failures, and a fresh successful poll.
`scriba server status --env prod --json` reported schema 8.

The installed `policy explain` and `outbox list` commands read 15 persisted
policy evaluations and 40 delivered outbox messages. Before and after the
candidate smoke, policy event and outbox aggregates remained at zero policy
events, 40 delivered messages, and 41 delivery attempts. Redacted explanation
output removed account references. The service journal contained no startup or
SQLite error, and all pending, leased, expired, and dead-letter queue counts
remained zero.

This inspection deployment does not alter the migration evidence above.

## Wave 2 release proof

Commit `6bfbcb3` closed the final non-production release proof. A composed
bootstrap and transition fixture now exercises the real policy evaluator and
atomic SQLite apply path across all four rule kinds. It asserts the expected
event ordering, typed legacy projections, semantic policy-event fields,
deterministic outbox identity and target, versioning, and byte-identical policy
event/outbox payloads.

A second integration fixture produces a policy transition through that same
store path, then uses the real SQLite outbox and Telegram SDK against a local
HTTP server. The first send times out, the same row returns to pending with one
attempt and bounded backoff, and its next claim completes as delivered with two
attempts and provider message ID `42`. Repeated race runs, the full Go race
suite, `just check`, and all 18 Swift tests passed. The Wave 2 release gate is
closed.
