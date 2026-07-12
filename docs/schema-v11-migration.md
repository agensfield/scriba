# Schema v11 Migration and Activation

Date: 2026-07-13

Schema v11 adds durable configured profiles, provider-account history, isolated
poll health, and canonical outbox attribution. This receipt covers the exact
stopped-service schema-v10 artifact used for live activation and the follow-up
cancellation-log roll-forward.

## Authoritative source and candidate

The service was stopped before creating the source backup:

- backup: `/home/arda/.local/state/scriba/backups/scriba-server-backup-20260712T234321.545656181Z-5a939af723c0.sqlite`
- source SHA-256: `b899c6b54f2f99bf1d14d04b26d53154bea4145beb20c8059d12d24338d5b3cf`
- source schema: 10
- candidate commit: `ca0dbf0`
- candidate SHA-256: `a37d774a0b29249740afca3c9d903d2e21dc66855d8c57e75dcf3d099e281977`
- evidence directory: `/home/arda/.local/state/scriba/cutovers/v11-live-20260712T234321Z`

## Migration and rollback proof

The exact stopped-service source was copied before the candidate opened it.
The proof established:

- first open migrated schema 10 to 11 and second open was idempotent;
- `quick_check` returned `ok` and `foreign_key_check` returned no rows;
- every pre-existing business table retained data hash
  `4fad005aea77c450748c41f7f1848b6d776d7fa3a11a5b790d2f8751ea0149c2`;
- the outbox projection excluding the intentional new `profile_ref` retained
  hash `fb5225c7581b10d8e96eb4e4e532d0daa1e06aa35ac1457fef0c9ca44b0acb05`;
- all 43 outbox rows survived, all 33 attributable rows were backfilled, and
  none remained unattributed;
- one compatibility profile and one current provider-account mapping were
  created for the existing config-v1 deployment;
- the schema-v10 binary refused the migrated copy and opened an untouched
  schema-v10 rollback copy successfully;
- the source and rollback copies initially shared the exact source SHA-256.

## Live activation

The candidate migrated the live database to schema 11. Post-activation smokes
proved:

- healthy `default` profile selection through refresh, health, stats, CLI
  context, Unix HTTP context/SSE, and both stdio MCP tools;
- CLI and HTTP context parity after removing only dynamic time fields;
- replay through sequence 3 followed by a reconnect with no duplicate event;
- no auth path, token, account reference, or private provenance in the public
  agent surfaces;
- owner-only `0700` state directory plus `0600` socket and database;
- `quick_check=ok`, no foreign-key violations, and no unattributed outbox rows;
- all 12 Telegram commands registered, including `/profiles` and the three
  profile-selecting commands;
- two successful restarts with no pending, leased, or dead queue work.

Local composed fixtures use two independent auth files and prove profile route,
sequential poll, durable mapping, and agent-context isolation. The devbox has
only one real Codex auth file, so the roadmap's two-independent-real-account
release proof remains an explicit external gate rather than a claimed result.

## Cancellation-log roll-forward

CI run `29214099896` passed race/vet, lint/security, and macOS Go/Swift for
commit `538d557`. That commit suppresses only the expected `context canceled`
poll warning during an orderly service stop.

The already-migrated service was rolled forward without another database
migration:

- live commit: `538d557`;
- Linux amd64 SHA-256: `9c3d1c6b6f2f0f646d845809313fa8d86901de412fc2a10578efcb5f3cb6c916`;
- preserved pre-roll-forward binary SHA-256:
  `a37d774a0b29249740afca3c9d903d2e21dc66855d8c57e75dcf3d099e281977`;
- schema migration ledger remained 11 and `quick_check` remained `ok`;
- two more restarts returned healthy `default` profile state with zero
  pending, leased, or dead inbox/outbox work;
- the post-deploy journal contained no poll failure or cancellation warning.

Wave 3.2 is implemented, deployed for the compatibility profile, and locally
proven with two isolated fixture accounts. Only the explicitly external
two-real-account live proof remains.
