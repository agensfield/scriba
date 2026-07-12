# Budget and Policy Checkpoint

This document records the committed Wave 2.1 code boundary. It does not claim
that schema v8 or policy evaluation has been deployed, or that the resident
server has cut over from the existing reset/warning path.

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

At this checkpoint the evaluator and schema exist, but policy persistence and
event production are not documented as the active runtime path. Runtime
cutover, inspection commands, migration/deploy receipts, and live Wave 2 gates
remain release work.
