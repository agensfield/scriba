# Scriba CLI

The CLI is the alpha contract for both humans and agents. Human-friendly output
is the default; pass `--json` for the machine-readable agent contract.

## Root

```sh
bun run scriba
bun run scriba doctor
bun run scriba doctor --no-remote --json
bun run scriba status
bun run scriba status --fast
bun run scriba status --no-remote
bun run scriba status --redact --json
bun run scriba schema
bun run scriba cache status
bun run scriba cache reset
bun run scriba cache prune
bun run scriba cache vacuum
bun run scriba telegram alerts
```

Bare `scriba` is equivalent to `scriba status`. `status` composes local log
summaries and remote provider-window probes when auth is available. It writes a
derived JSON snapshot and SQLite scan stats unless `--no-cache` is passed.

`doctor` checks local source directories, auth files, remote reachability, cache
schema/WAL state, cache size, and latest snapshot age. It reports `ok`,
`degraded`, or `broken` in human and JSON output.

Claude remote auth checks `~/.claude/.credentials.json` first, then the Claude
Code macOS Keychain services. Normal `--fast` and `--no-remote` status reads do
not touch provider APIs or prompt-capable Keychain password reads.

`--fast` reads the cached status snapshot only. It is intended for menu bar,
Telegram, and agent reads that should not trigger a foreground scan. `--no-remote`
skips provider API probes. `--redact` removes paths, account identifiers, and
emails from output before sharing.

`cache vacuum` truncates WAL state before and after compaction, closes the cache,
then waits briefly for settled filesystem sizes. Its JSON reports `beforeBytes`,
`afterBytes`, signed `deltaBytes`, `reclaimedBytes`, and `grewBytes`; SQLite can
occasionally rebuild into a slightly larger database file, so growth is explicit
rather than hidden.

Stable JSON shape example:

```json
{
  "schemaVersion": "scriba.alpha.v1",
  "generatedAt": "2026-05-05T13:00:00.000Z",
  "providers": [
    {
      "providerId": "codex",
      "displayName": "Codex",
      "state": "ok",
      "lines": [],
      "provenance": []
    }
  ]
}
```

Remote status lines currently include Claude peak-hours/window metrics and Codex
plan/window metrics. Five-hour windows are labeled `5h limit`; weekly windows
are labeled `Weekly limit`. Human output shows percentage bars as used capacity
and includes reset timing when the provider API returns it. Standalone balances,
such as Codex credits remaining, are represented as `amount` lines in JSON
instead of fake progress bars.

## Package Execution

The published package builds to JavaScript in `dist` and exposes `scriba` as the
package binary:

```sh
bunx @agensfield/scriba status
npx @agensfield/scriba status
pnpm dlx @agensfield/scriba status
yarn dlx @agensfield/scriba status
```

Cache-backed commands use `libsql`, so the same built package works under Bun
and Node without runtime-specific SQLite imports.

## Claude

```sh
bun run scriba claude summary
bun run scriba claude daily
bun run scriba claude weekly
bun run scriba claude monthly
bun run scriba claude sessions
bun run scriba claude blocks
```

Report commands support `--since` and `--until`, accepting either full
timestamps or `YYYY-MM-DD` dates.

## Codex

```sh
bun run scriba codex summary
bun run scriba codex daily
bun run scriba codex weekly
bun run scriba codex monthly
bun run scriba codex sessions
bun run scriba codex session
```

Codex local scanning reads `${CODEX_HOME:-~/.codex}/sessions/**/*.jsonl`.

## Benchmark

```sh
bun run scriba bench ccusage --provider all
bun run scriba bench ccusage --provider codex --execute --timeout-ms 30000
bun run scriba bench ccusage --provider codex --out bench.json
```

Without `--execute`, the benchmark only summarizes local dataset size and prints
the reference command plan. With `--execute`, each reference command gets its own
timeout and output samples are capped.

## Telegram

```sh
bun run scriba telegram alerts
bun run scriba telegram alerts --send
```

`telegram alerts` builds the current status snapshot and evaluates configured
thresholds. `--send` requires `telegram.chatId` in config and the bot token in
the configured environment variable.
