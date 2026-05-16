# Scriba Current State

Date: 2026-05-16

## Project Shape

Scriba is the first child project under Agensfield. Agensfield is the umbrella
for long-running agent infrastructure work. Scriba starts narrower: a fast,
minimal Claude Code and Codex usage tracker.

The immediate pain is concrete: `ccusage`/`openusage` can consume pathological
memory on large local Codex/Claude histories. Scriba should compute the same
class of usage answers while staying resource-sane.

## Alpha Decisions

- The repo is a Bun monorepo from day one.
- Alpha core lives in `packages/scriba`; the macOS menu bar app stays a thin
  native consumer over the same CLI/status contract.
- Library and CLI stay together until there is real package-boundary pain.
- Source of truth remains external: Claude/Codex logs and provider APIs.
- Scriba stores only read-only derived state.
- Use a hybrid-lite cache: SQLite for scan/index/aggregate state, JSON
  snapshots for app, CLI status, Telegram, and agent reads.
- Implement alpha core in TypeScript/Bun for package importability and JS
  ecosystem use.
- Use Swift/AppKit/SwiftUI for the macOS menu bar app.
- Expose a semi-public adapter interface in alpha, marked unstable.
- Ship `@agensfield/scriba` plus the `scriba` binary.
- Support both a composed `scriba status` command and provider-specific
  commands such as `scriba claude daily` and `scriba codex sessions`.
- Telegram alpha is alert-only, designed so queryable bot commands can come
  later.

## Required Alpha Surfaces

Implemented alpha foundation:

- Bun workspace with Biome, TypeScript, Vitest, and SQLite/cache tests.
- `@agensfield/scriba` package and `scriba` CLI.
- Dist-based package build for npm/Bun/pnpm/Yarn consumers, with one `libsql`
  cache engine for Bun and Node.
- Config discovery and schema metadata.
- `scriba doctor` for local source/auth/remote/cache health.
- Claude/Codex local JSONL scanners.
- Daily, weekly, monthly, session, and Claude block report builders.
- Human-friendly CLI output by default, with `--json` for agents.
- SQLite derived cache plus JSON status snapshot writer.
- Codex file-event cache keyed by source path, size, and mtime for fast warm
  report refreshes.
- Remote provider probes for Claude/Codex usage windows.
- Telegram alert evaluator and sender.
- Light `ccusage` benchmark harness.
- Provider descriptor layer for labels, default paths, auth hints, reports, and
  remote probes.
- Native Swift/AppKit menu bar shell in `apps/macos`, using the TypeScript CLI
  as its data engine and preserving Scriba core ownership in `packages/scriba`.
- Native usage-history submenu for the macOS app: each successful status
  refresh records local provider progress samples, and the menu exposes a
  hoverable history chart/details surface.
- macOS weekly-reset watcher: the menu bar app persists weekly limit baselines,
  refreshes every 10 minutes, and sends a local notification when a weekly
  provider limit appears to reset before its previous reset time.

Local token/cost reports:

- Claude: daily, weekly, monthly, sessions, blocks, and compact status view.
- Codex: daily, weekly, monthly, sessions.
- Common rollups: today, yesterday, last 30 days, models, cache tokens, costs,
  sessions/projects where available.

Remote/window metrics borrowed from OpenUsage/CodexBar references:

- Claude: Peak Hours, 5h limit, Weekly limit, OAuth Apps, Sonnet, Claude
  Design, Claude Routines, Extra Claude window, Extra usage spent.
- Codex: Plan, 5h limit, Weekly limit, Spark 5h, Spark weekly, Review 5h,
  Review weekly, Credits left.

## Invariants

- Bounded-memory cold scan. The scanner reads JSONL line-by-line and the main
  report/status paths aggregate from async iterators. Claude blocks still sorts
  raw events because the block algorithm is chronological.
- Fast incremental refresh after first scan. Claude and Codex report/status
  paths now reuse cached parsed file events for unchanged files.
- SQLite cache uses WAL plus a busy timeout so concurrent cached commands do
  not immediately fail on transient reader/writer overlap.
- Cache lifecycle has a schema version, cache size/status output, stale
  file-event pruning, and `scriba cache vacuum`.
- No `ccusage` subprocess dependency in the normal core path.
- CLI output is agent-grade: JSON, predictable schema, source provenance,
  freshness, auth/cache/error state.
- Package execution is ecosystem-compatible: the default binary is built JS for
  npm/pnpm/Yarn/Bun installs, while cache features use `libsql` instead of a
  runtime-specific SQLite module.
- `scriba status --fast` reads only the cached status snapshot, and `--no-remote`
  skips provider API probes.
- The macOS app uses `status --fast --json` for first paint, then full
  `status --json` refreshes in the background, so launch stays responsive while
  limit/reset notifications still use fresh provider state.
- macOS usage history is derived from the same status refreshes and stored
  locally in `UserDefaults`; it deduplicates unchanged samples inside the
  refresh window and keeps a bounded recent history per provider.
- macOS packaging builds the TypeScript CLI before staging the app, writes
  package/git metadata into `Info.plist`, signs helper/native binaries before
  signing the app, strips xattrs/AppleDouble files, and smokes the bundled
  helper under a stripped LaunchServices-like `PATH`. The fallback helper is a
  native shim named `scriba` and includes a bundled Bun runtime, so it does not
  require a user-installed Node/Bun runtime when no acceptable system `scriba`
  is present.
- macOS distribution has a local zip artifact path: `package_zip.sh` validates
  the staged app, zips with `ditto --norsrc`, writes a `.sha256`, extracts to a
  temp dir, verifies the extracted app signature, and smokes the extracted
  bundled helper.
- macOS/runtime temp hygiene is part of shippability: resolver subprocess
  output dirs are removed after each run, dist extraction dirs are removed on
  exit, package smoke temp consumers are removed on success/failure, and Swift
  resolver tests clean their fake app bundles.
- `--redact` redacts local paths, account identifiers, and emails from JSON and
  human output.
- Cache deletion is safe: `scriba cache reset` can delete derived state and the
  next scan rebuilds from source logs.

## Reference Findings

`ccusage`:

- Claude loader streams JSONL lines but still accumulates all parsed entries
  before grouping.
- Claude reports include daily, weekly, monthly, sessions, and blocks.
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
- Scriba `status --json --no-cache`: 8.50s real, 1,023,508,480 bytes max RSS.
- Scriba `status --json` cold after cache reset: 8.94s real, 992,968,704
  bytes max RSS.
- Scriba `status --json` warm after cache population: 1.00s real, 218,202,112
  bytes max RSS.
- Scriba `claude daily --json` warm with file-event cache: 0.20s real,
  121,667,584 bytes max RSS.

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
  adapting for the native macOS app state.
- Its polished menu comes from a real `NSMenu`, AppKit-backed menu rows,
  vibrant hosted SwiftUI cards, and restrained blue accents. ScribaBar now uses
  the same broad architecture; live visual polish still has to be judged from
  real menu screenshots, not static SwiftUI preview renders.
- CodexBar's shippability scripts are useful beyond signing/notarization:
  avoid stale running bundles, make packaging fail on missing resources, seed
  runtime paths for LaunchServices-launched apps, sign helpers before the app,
  and validate the newly packaged app is the one that stayed running.
- Codex OAuth usage API fields observed in the ref: `plan_type`,
  `rate_limit.primary_window`, `rate_limit.secondary_window`,
  `code_review_rate_limit`, and `credits.balance`. Real local probe on
  2026-05-05 returned plan `prolite`, 18% session used, and 3% weekly used;
  code-review and credits were absent/null for the current account response.
- Claude OAuth usage refs include aliases for `seven_day_sonnet`,
  `seven_day_oauth_apps`, Claude Design, Claude Routines, extra spend, and
  CodexBar's local peak-hours calculation. Claude auth now checks the standard
  `~/.claude/.credentials.json` file first and falls back to the Claude Code
  macOS Keychain service (`Claude Code-credentials`, including
  `CLAUDE_CONFIG_DIR` hashed variants). Real local proof on 2026-05-05 loaded
  OAuth from Keychain and returned Claude remote windows without degrading the
  provider.

## Open Questions

- Whether ad-hoc signed `.app` distribution is enough for alpha users before
  adding notarization/Sparkle.
- Whether to split CLI/Telegram/desktop into separate packages after first npm
  publish pressure, or keep the single package longer.
- Public command names once table output settles.
