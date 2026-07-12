# Budget and Policy Checkpoint

This document records the Wave 2.1 budget and policy boundary. Schema v8 and
atomic policy evaluation are live on the devbox. The read-only policy
inspection CLI surfaces were deployed at commit `a168e34`. Exact
fixture-transition and Telegram retry-through-outbox proof landed at commit
`6bfbcb3`, closing the Wave 2 release gate.

## Budget Surfaces

`scriba codex budget` and `scriba claude budget` fetch fresh provider quota
windows and emit human output or the typed `scriba.budget.v1` JSON contract.
They intentionally do not accept `--fast` because cached status is not a sound
starting point for a pacing decision.

All burn and allowance rates are quota percentage points. They are not token
rates and local token logs cannot affect the result. For example, `3pp/h` means
that provider-reported used quota increased by three percentage points per
hour. Reports include remaining percentage points, cycle and recent burn, a
conservative pace, safe hourly/daily allowance, projected exhaustion, temporal
margin, risk, freshness, confidence, and machine-readable reason codes.

Codex recent burn considers matching durable observations between 10 minutes
and 24 hours before the current observation. Reset changes, counter decreases,
and incompatible windows are excluded with explicit reasons. Claude currently
has no durable quota history, so its confidence is based on the current cycle.
Derived budget reports are recomputed and never stored in SQLite.

The JSON Schema is
[`schemas/budget.schema.json`](../schemas/budget.schema.json).

## Schema v8

Schema v8 adds:

- `policy_states`, keyed by provider, account, policy revision, config hash,
  rule, and subject, with structured state and evaluation JSON;
- `policy_events`, with unique semantic keys and unique event-kind/event-id
  correlations, plus versioned payload JSON;
- constrained rule kinds, account foreign keys, and lookup indexes for policy
  state, account history, and event inspection.

Store open validates required columns, constraints, and indexes. A database
whose migration version is newer than the binary supports is refused rather
than opened optimistically.

Schema downgrade is restore-only. Before deploying a binary that can migrate
to v8, retain a verified pre-migration backup. To return to an older binary,
restore that backup; do not attempt to mutate a v8 database back into v7.

## Policy Core

The pure policy package accepts only four rule kinds:

- `remaining_checkpoint` for descending remaining-quota thresholds;
- `reset_transition` for reset-window transitions with bounded clock/due
  jitter;
- `grant_available` for newly available reset grants;
- `grant_expiry_checkpoint` for descending day-before-expiry thresholds.

The `current` compatibility preset encodes primary remaining checkpoints at
20, 10, 5, and 0 percent; weekly reset transition behavior; newly available
grants; and grant-expiry checkpoints at 5, 3, and 1 days. The first observation
is a bootstrap: it establishes state and explanations without emitting policy
events. Later evaluations use semantic identities so repeated evaluation is
deterministic and deduplicable.

The resident server now makes policy evaluation the active runtime path. A
poll loads prior policy state, evaluates the `current` preset, and persists
state, semantic events, legacy-compatible business events, and notification
intents atomically. Account bootstrap and policy bootstrap are separate: a
first policy evaluation establishes state and emits nothing, including for an
account already known to the older polling path. The cutover did not run dual
evaluation or replay historical alerts.

## Read-only Inspection

The operator surfaces do not evaluate policies, claim outbox messages, or
mutate SQLite:

- `policy validate <file>` strictly validates a supplied config and emits
  `scriba.policy-validate.v1`;
- `policy list [--config <file>]` lists the built-in `current` preset or a
  validated supplied config and emits `scriba.policy-list.v1`;
- `policy explain` reads persisted state/evaluation explanations with exact
  provider, account, and rule filters and emits `scriba.policy-explain.v1`;
- `outbox list` reads delivery envelopes with exact id, status, and target
  filters and emits `scriba.outbox-list.v1` without leasing work.

The two state-backed commands accept a bounded 1..1000 result limit and support
field-aware `--redact` output for identifiers, stored JSON bodies, delivery
payloads, lease/provider metadata, and errors. Their Draft 2020-12 schemas are
checked in under [`schemas/`](../schemas/).

An invalid JSON-mode validation exits nonzero after emitting a typed
`valid: false` result. Read-only state access preserves the main database,
schema, rows, attempts, and leases; SQLite's own live-WAL coordination may
touch transient sidecar bookkeeping.

The live migration and deployment evidence is recorded in
[`schema-v8-migration.md`](schema-v8-migration.md). The release proof drives a
bootstrap and a multi-kind transition through the real evaluator and SQLite
transaction, then drives a policy notification through real SQLite claim,
failure backoff, reclaim, Telegram SDK decoding, and fenced success.
