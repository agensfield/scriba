# Scriba CLI

The CLI is the alpha contract for both humans and agents. Human-friendly output
is the default; pass `--json` for the machine-readable agent contract.

## Root

```sh
bun run scriba status
bun run scriba schema
bun run scriba cache status
bun run scriba cache reset
bun run scriba telegram alerts
```

`status` composes local log summaries and remote provider-window probes when
auth is available. It writes a derived JSON snapshot and SQLite scan stats
unless `--no-cache` is passed.

Remote status lines currently include Claude peak-hours/window metrics and
Codex plan/window metrics. Standalone balances, such as Codex credits remaining,
are represented as `amount` lines in JSON instead of fake progress bars.

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
