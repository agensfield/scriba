# Scriba

Fast, local-first usage tracking for Claude Code and Codex.

Scriba is a small Go CLI and resident Telegram bot for people who live in
agent terminals. It reads local Claude/Codex session logs, checks the
ChatGPT/Codex subscription usage backend when your local Codex auth is
available, and keeps enough SQLite state to send useful reset and limit
notifications without becoming the source of truth.

The macOS menu bar app remains archived in place under `apps/macos`, but it is
outside the active product roadmap. The maintained product surfaces are the
CLI, resident server, Telegram, Unix HTTP/SSE API, and stdio MCP.

## Features

- Local Claude Code and Codex usage reports: daily, weekly, monthly, sessions,
  and summaries. Codex reports separate effective tokens from full cached
  traffic, show every materially used model, and estimate standard API cost.
- Live Codex limit windows from the logged-in ChatGPT/Codex backend.
- `scriba codex reset-grants` for available reset grants and each grant's
  expiration timestamp.
- `scriba codex reset` for an explicitly confirmed redemption of the available
  grant expiring soonest, with a non-mutating `--dry-run` preview.
- `scriba codex profile` for ChatGPT/Codex profile token activity, streaks,
  reasoning mix, and top skills/plugins.
- Explicit additional Codex buckets, including Spark when OpenAI exposes it.
- Resident Telegram bot with `/profiles`, profile-aware `/limits`, `/grants`,
  confirmed `/reset`, `/profile`, plus `/refresh`, `/health`,
  `/stats`, `/lastreset`, `/settings`, and radar commands.
- Telegram notifications for weekly resets, deduplicated pacing risk, low remaining limits, reset-grant
  expiry checkpoints, service health, and Codex Radar probability milestones.
- Local SQLite cache/state. Source logs and provider APIs remain authoritative.
- JSON-only `scriba context --json [--profile <id>]` for an allowlisted,
  read-only agent view of
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
scriba codex reset --dry-run
scriba codex profile
scriba context --json
scriba context --json --profile work
scriba update --check
```

Use `--json` when another program is consuming the output:

```sh
scriba codex limits --json
scriba codex reset-grants --json
scriba codex reset --dry-run --json
scriba codex profile --json
```

`scriba codex limits`, `scriba codex reset-grants`, `scriba codex reset`, and
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

`/reset [profile]` and the Reset limits buttons fetch a fresh reset preview,
select the available grant expiring soonest, and show its exact id and expiry.
No grant is spent until the originating Telegram user presses Confirm reset.
Confirmations are bound to that chat and user, expire after ten minutes, and
reuse one idempotency key across safe retries. Cancel never redeems.

With config v2 profiles, `/profiles` lists enabled IDs and health. Use
`/limits work`, `/grants work`, `/reset work`, or `/profile work` to select one
mapped Codex account; omitting the argument selects the configured default. Telegram never
accepts provider account references or auth paths as selectors.

For systemd user service setup, scheduled backups, restore drills, and linger,
see [Operations](docs/operations.md). BotFather setup lives in
[Telegram Bot](docs/telegram.md).

## Development

```sh
just check
just install-cli
just cli codex limits
just cli codex reset-grants
just cli codex reset --dry-run
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
- [Operations](docs/operations.md)
- [Security](SECURITY.md)
- [Current State](docs/current-state.md)
- [Agent Context](docs/agent-context.md)
- [Benchmarks](docs/benchmarks.md)
