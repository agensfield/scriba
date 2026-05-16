# Scriba CLI

The CLI is the contract for both humans and agents. Human-friendly output is
the default; pass `--json` for machine-readable output.

## Root

```sh
scriba
scriba --version
scriba doctor
scriba doctor --no-remote --json
scriba status
scriba status --fast
scriba status --no-remote
scriba status --redact --json
scriba schema
scriba cache status
scriba cache reset
scriba cache prune
scriba cache vacuum
scriba telegram alerts
```

Bare `scriba` is equivalent to `scriba status`.

`status` composes local log summaries and remote provider-window probes when
auth is available. It writes a derived JSON snapshot and SQLite scan stats
unless `--no-cache` is passed.

`doctor` checks local source directories, auth paths, remote reachability,
cache schema/WAL state, cache size, and latest snapshot age. It reports `ok`,
`degraded`, or `broken`.

`--fast` reads the cached status snapshot only. It is intended for the menu bar,
Telegram, and agent reads that should not trigger a foreground scan.

## Reports

```sh
scriba claude summary
scriba claude daily
scriba claude weekly
scriba claude monthly
scriba claude sessions
scriba claude blocks

scriba codex summary
scriba codex daily
scriba codex weekly
scriba codex monthly
scriba codex sessions
```

Report commands support `--since` and `--until`, accepting full timestamps or
`YYYY-MM-DD` dates.

## Package Execution

Scriba is a Go binary. Build locally with:

```sh
go build -o .build/scriba ./cmd/scriba
```

The macOS app bundles this native binary directly at
`ScribaBar.app/Contents/Helpers/scriba`.
