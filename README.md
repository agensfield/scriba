# Scriba

Fast, local-first usage tracking for Claude Code and Codex.

Scriba is a small Go CLI and resident Telegram bot for people who live in
agent terminals. It reads local Claude/Codex session logs, checks the
ChatGPT/Codex subscription usage backend when your local Codex auth is
available, and keeps enough SQLite state to send useful reset and limit
notifications without becoming the source of truth.

The macOS menu bar app still exists under `apps/macos`, but the mainline
product surface is now the CLI plus the resident `scriba server` process.

## Features

- Local Claude Code and Codex usage reports: daily, weekly, monthly, sessions,
  and summaries.
- Live Codex limit windows from the logged-in ChatGPT/Codex backend.
- `scriba codex reset-grants` for available reset grants and each grant's
  expiration timestamp.
- Explicit additional Codex buckets, including Spark when OpenAI exposes it.
- Resident Telegram bot with `/limits`, `/refresh`, `/health`, `/stats`,
  `/lastreset`, `/settings`, and radar commands.
- Telegram notifications for weekly resets, low remaining limits, reset-grant
  expiry checkpoints, service health, and Codex Radar probability milestones.
- Local SQLite cache/state. Source logs and provider APIs remain authoritative.
- Human-readable terminal output by default, JSON with `--json` for scripts and
  agents.

## Install

With Go 1.26+:

```sh
go install github.com/agensfield/scriba/cmd/scriba@latest
scriba --version
scriba doctor
```

From a checkout:

```sh
go build -o .build/scriba ./cmd/scriba
go test ./...
go vet ./...
```

See [Install](docs/install.md) for GitHub binary and Homebrew tap notes.

## Quick Start

```sh
scriba
scriba status
scriba doctor

scriba claude weekly
scriba codex summary
scriba codex limits
scriba codex reset-grants
```

Use `--json` when another program is consuming the output:

```sh
scriba codex limits --json
scriba codex reset-grants --json
```

`scriba codex limits` and `scriba codex reset-grants` use local Codex OAuth
state from `${CODEX_HOME:-~/.codex}/auth.json`. An OpenAI API key cannot expose
these ChatGPT subscription windows.

## Telegram Bot

The resident server is the Telegram path:

```sh
scriba config init
scriba config telegram --enable --chat-id "$TELEGRAM_CHAT_ID" --bot-token-env SCRIBA_TELEGRAM_BOT_TOKEN
scriba server run --env prod
```

For systemd user service setup and BotFather notes, see
[Telegram Bot](docs/telegram.md).

## Development

```sh
just check
just install-cli
just cli codex limits
just cli codex reset-grants
```

`just check` is the core Go gate. macOS menu bar recipes remain available:

```sh
just macos-test
just macos-release
just check-all
```

## Docs

- [Install](docs/install.md)
- [Telegram Bot](docs/telegram.md)
- [CLI](docs/cli.md)
- [Configuration](docs/config.md)
- [Current State](docs/current-state.md)
- [Benchmarks](docs/benchmarks.md)
