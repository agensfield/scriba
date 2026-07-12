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
  and summaries. Codex reports separate effective tokens from full cached
  traffic, show every materially used model, and estimate standard API cost.
- Live Codex limit windows from the logged-in ChatGPT/Codex backend.
- `scriba codex reset-grants` for available reset grants and each grant's
  expiration timestamp.
- `scriba codex profile` for ChatGPT/Codex profile token activity, streaks,
  reasoning mix, and top skills/plugins.
- Explicit additional Codex buckets, including Spark when OpenAI exposes it.
- Resident Telegram bot with `/limits`, `/grants`, `/profile`, `/refresh`, `/health`,
  `/stats`, `/lastreset`, `/settings`, and radar commands.
- Telegram notifications for weekly resets, low remaining limits, reset-grant
  expiry checkpoints, service health, and Codex Radar probability milestones.
- Local SQLite cache/state. Source logs and provider APIs remain authoritative.
- JSON-only `scriba context --json` for an allowlisted, read-only agent view of
  quota windows, budgets, grants, source freshness, and minimized policy events.
- `scriba update --check` and `scriba update` for tagged release updates,
  with Homebrew-managed installs routed to `brew upgrade scriba`.
- Human-readable terminal output by default, JSON with `--json` for scripts and
  agents.

## Install

On macOS with Homebrew:

```sh
brew install agensfield/tap/scriba
scriba --version
scriba doctor
```

On macOS or Linux with the install script:

```sh
curl -fsSL https://raw.githubusercontent.com/agensfield/scriba/main/scripts/install.sh | sh
```

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

See [Install](docs/install.md) for GitHub binaries and alternate install
paths.

## Quick Start

```sh
scriba
scriba status
scriba doctor

scriba claude weekly
scriba codex summary
scriba codex daily --timezone Europe/Istanbul
scriba codex limits
scriba codex reset-grants
scriba codex profile
scriba context --json
scriba update --check
```

Use `--json` when another program is consuming the output:

```sh
scriba codex limits --json
scriba codex reset-grants --json
scriba codex profile --json
```

`scriba codex limits`, `scriba codex reset-grants`, and
`scriba codex profile` use local Codex OAuth state from
`${CODEX_HOME:-~/.codex}/auth.json`. An OpenAI API key cannot expose these
ChatGPT subscription windows or profile stats.

## Telegram Bot

The resident server is the Telegram path:

```sh
scriba config init
scriba config telegram --enable --chat-id "$TELEGRAM_CHAT_ID" --bot-token-env SCRIBA_TELEGRAM_BOT_TOKEN
scriba server run --env prod
```

The bot's `/grants` command and Grants inline button show every available
Codex reset grant with its title, reset type, status, granted time, expiry,
remaining lifetime, and full credit id. Use `/refresh` first when you need a
new live provider observation rather than the resident server's latest stored
poll.

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
- [Agent Context](docs/agent-context.md)
- [Benchmarks](docs/benchmarks.md)
