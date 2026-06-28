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
scriba config path
scriba config show --json
scriba config init
scriba config telegram --enable --chat-id <id> --bot-token-env SCRIBA_TELEGRAM_BOT_TOKEN
scriba cache status
scriba cache reset
scriba cache prune
scriba cache vacuum
scriba update --check
scriba update
scriba telegram alerts
scriba telegram alerts --refresh
scriba telegram reset --send --provider codex --label weekly --message "🎉 Tibo just reset limits! 🎊"
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

## Config

`scriba config telegram` edits the same config file used by the macOS menu bar
settings window. Bot tokens can be stored in the config for app use, or supplied
through `telegram.botTokenEnv` for terminal-only workflows.

`scriba telegram alerts` reads the cached status snapshot by default so menubar
actions stay fast. Pass `--refresh` when you explicitly want it to rebuild status
before evaluating alerts.

`scriba telegram reset` is the small one-shot send path used by ScribaBar when a
reset heuristic fires. It reads the same Telegram config and only sends when
Telegram is enabled.

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
scriba codex limits
scriba codex reset-grants
scriba codex limits --fast
```

Report commands support `--since` and `--until`, accepting full timestamps or
`YYYY-MM-DD` dates.

`scriba codex limits` skips local log scanning and only fetches Codex usage
windows from the logged-in ChatGPT/Codex backend. It reads Codex OAuth state
from `${CODEX_HOME:-~/.codex}/auth.json`; OpenAI API key auth cannot expose
these ChatGPT subscription windows. The live payload includes primary Codex
windows, explicit additional model windows such as Spark, and the available
rate-limit reset grant count. When the read-only reset-credit metadata endpoint
answers, Scriba also shows the earliest available grant expiry. Pass `--fast`
to read the last cached `scriba status` snapshot instead of making a network
request.

`scriba codex summary` keeps the local usage summary and, unless `--no-remote`
is passed, appends the same live Codex limits and reset-grant metadata.

`scriba codex reset-grants` shows available rate-limit reset grants as a focused
view, including every available grant's `grantedAt` and `expiresAt` timestamp
when OpenAI exposes the read-only reset-credit metadata. The short alias is
`scriba codex grants`.

The resident server stores every available reset credit from the read-only
metadata endpoint when present. Telegram grant-expiry warnings are deduped by
grant id, expiry timestamp, and checkpoint, and fire once at 5 days, 3 days,
and 1 day before that grant's own expiry.

Human metric output aligns labels within each rendered provider/output, so
progress bars start in the same column even when labels differ in length.

## Updates

`scriba update --check` compares the current binary to the latest GitHub tag and
prints the detected install manager/path. `scriba update` installs the latest
tag with:

```sh
go install github.com/agensfield/scriba/cmd/scriba@<latest-tag>
```

When the binary resolves under a Homebrew `Cellar/scriba` path, Scriba refuses
self-update and points the user to `brew upgrade scriba`.

## Package Execution

Scriba is a Go binary. Build locally with:

```sh
go build -o .build/scriba ./cmd/scriba
```

The macOS app bundles this native binary directly at
`ScribaBar.app/Contents/Helpers/scriba`.
