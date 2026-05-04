# Scriba Current State

Date: 2026-05-05

## Project Shape

Scriba is the first child project under Agensfield. Agensfield is the umbrella
for long-running agent infrastructure work. Scriba starts narrower: a fast,
minimal Claude Code and Codex usage tracker.

The immediate pain is concrete: `ccusage`/`openusage` can consume pathological
memory on large local Codex/Claude histories. Scriba should compute the same
class of usage answers while staying resource-sane.

## Alpha Decisions

- Source of truth remains external: Claude/Codex logs and provider APIs.
- Scriba stores only read-only derived state.
- Use a hybrid-lite cache: SQLite for scan/index/aggregate state, JSON
  snapshots for app, CLI status, Telegram, and agent reads.
- Implement alpha core in TypeScript/Bun for package importability and JS
  ecosystem use.
- Use Tauri for the menu bar app.
- Expose a semi-public adapter interface in alpha, marked unstable.
- Ship `@agensfield/scriba` plus the `scriba` binary.
- Support both a composed `scriba status` command and provider-specific
  commands such as `scriba claude daily` and `scriba codex sessions`.
- Telegram alpha is alert-only, designed so queryable bot commands can come
  later.

## Required Alpha Surfaces

Local token/cost reports:

- Claude: daily, weekly, monthly, sessions, blocks, compact statusline-style
  view.
- Codex: daily, monthly, sessions.
- Common rollups: today, yesterday, last 30 days, models, cache tokens, costs,
  sessions/projects where available.

Remote/window metrics borrowed from OpenUsage references:

- Claude: Session, Weekly, Peak Hours, Sonnet, Claude Design, Extra usage
  spent.
- Codex: Session, Weekly, Spark, Spark Weekly, Reviews, Credits.

## Invariants

- Bounded-memory cold scan.
- Fast incremental refresh after first scan.
- No `ccusage` subprocess dependency in the normal core path.
- CLI output is agent-grade: JSON, predictable schema, source provenance,
  freshness, auth/cache/error state.
- Cache deletion is safe: `scriba cache reset` can delete derived state and the
  next scan rebuilds from source logs.

## Reference Findings

`ccusage`:

- Claude loader streams JSONL lines but still accumulates all parsed entries
  before grouping.
- Claude reports include daily, weekly, monthly, sessions, blocks, and
  statusline.
- Codex loader reads each JSONL file fully, splits into lines, accumulates all
  events, then sorts.
- Codex local reports are daily, monthly, and sessions.

`openusage`:

- Wraps local token/cost metrics through `ccusage`.
- Adds remote usage/window APIs for Claude and Codex.
- Useful portable line schema: text, progress, badge.
- Good idea: static manifest/skeleton shape separate from dynamic output.

`CodexBar`:

- Reinforces separation between remote account/window metrics and local log
  cost history.
- Schedules slow cost scans outside the foreground refresh group.
- Uses a single central store plus provider descriptors, a pattern worth
  adapting for the Tauri app state.

## Open Questions

- Repo organization: single package first, Bun workspace, or monorepo from day
  one?
- Exact package split if monorepo: core, CLI, desktop, Telegram, adapters.
- SQLite library choice and native packaging implications for Tauri.
- Public command names and JSON schema versioning.
- Light `ccusage` benchmark command set and measurement tooling.
