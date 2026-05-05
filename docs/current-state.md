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

- The repo is a Bun monorepo from day one.
- Alpha code lives in `packages/scriba`; desktop/Tauri stays as a placeholder
  consumer until the core stabilizes.
- Library and CLI stay together until there is real package-boundary pain.
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

Implemented alpha foundation:

- Bun workspace with Biome, TypeScript, Vitest, and Bun SQLite tests.
- `@agensfield/scriba` package and `scriba` CLI.
- Config discovery and schema metadata.
- Claude/Codex local JSONL scanners.
- Daily, weekly, monthly, session, and Claude block report builders.
- Human-friendly CLI output by default, with `--json` for agents.
- SQLite derived cache plus JSON status snapshot writer.
- Codex file-event cache keyed by source path, size, and mtime for fast warm
  report refreshes.
- Remote provider probes for Claude/Codex usage windows.
- Telegram alert evaluator and sender.
- Light `ccusage` benchmark harness.

Local token/cost reports:

- Claude: daily, weekly, monthly, sessions, blocks, compact statusline-style
  view.
- Codex: daily, weekly, monthly, sessions.
- Common rollups: today, yesterday, last 30 days, models, cache tokens, costs,
  sessions/projects where available.

Remote/window metrics borrowed from OpenUsage/CodexBar references:

- Claude: Peak Hours, Session, Weekly, OAuth Apps, Sonnet, Claude Design,
  Claude Routines, Extra Claude window, Extra usage spent.
- Codex: Plan, Session, Weekly, Spark, Spark Weekly, Reviews, Review Weekly,
  Credits left.

## Invariants

- Bounded-memory cold scan. The scanner reads JSONL line-by-line and the main
  report/status paths aggregate from async iterators. Claude blocks still sorts
  raw events because the block algorithm is chronological.
- Fast incremental refresh after first scan. SQLite cache primitives exist;
  Codex report paths now reuse cached parsed file events for unchanged files.
  Claude report paths still need the same treatment.
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

Current benchmark evidence:

- Scriba `codex daily --json` cold after `scriba cache reset`: 6.98s real,
  626,802,688 bytes max RSS on the local 4.7 GB Codex history.
- Scriba `codex daily --json` warm with file-event cache: 0.24s real,
  156,205,056 bytes max RSS.
- `ccusage-codex daily --json` via `bunx -p @ccusage/codex@18.0.11`: 24.73s
  real, 6,893,535,232 bytes max RSS on the same history.

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
- Codex OAuth usage API fields observed in the ref: `plan_type`,
  `rate_limit.primary_window`, `rate_limit.secondary_window`,
  `code_review_rate_limit`, and `credits.balance`. Real local probe on
  2026-05-05 returned plan `prolite`, 18% session used, and 3% weekly used;
  code-review and credits were absent/null for the current account response.
- Claude OAuth usage refs include aliases for `seven_day_sonnet`,
  `seven_day_oauth_apps`, Claude Design, Claude Routines, extra spend, and
  CodexBar's local peak-hours calculation. The current machine does not expose
  a Claude OAuth credentials file at the standard path, so the live Claude API
  probe returned auth unavailable.

## Open Questions

- SQLite library choice and native packaging implications for Tauri.
- Whether to split CLI/Telegram/desktop into separate packages before first npm
  publish or keep the single package longer.
- Public command names once table output settles.
- Port file-event cache to Claude report paths if Claude histories become a
  bottleneck.
