# Telegram Bot

Scriba's Telegram integration is a resident `scriba server run` process. It
polls Codex limits, stores reset/alert state in SQLite, listens for Telegram
commands, and retries durable deliveries.

## Bot Setup

1. Create a bot with BotFather and keep the token private.
2. Send a message to the bot from the chat you want Scriba to use.
3. Get the chat id from Telegram, then configure Scriba:

```sh
export SCRIBA_TELEGRAM_BOT_TOKEN="123456:secret"
export TELEGRAM_CHAT_ID="123456789"

scriba config init
scriba config telegram --enable --chat-id "$TELEGRAM_CHAT_ID" --bot-token-env SCRIBA_TELEGRAM_BOT_TOKEN
scriba config show --redact
```

`telegram.botTokenEnv` is preferred for servers. `--bot-token` is supported,
but stores the token in `~/.config/scriba/config.json`.

## Run Locally

```sh
scriba server run --env prod
```

In Telegram:

```text
/health
/limits
/grants
/profile
/refresh
/stats
/lastreset
```

`/profile` fetches the ChatGPT/Codex profile stats backend on demand and
renders token activity, streaks, reasoning mix, and top skills/plugins in a
compact Telegram card.

`/grants` renders the full reset-grant inventory from the resident server's
latest durable observation: title, reset type, status, granted time, expiry,
remaining lifetime, and full credit id for every available grant. It is also
available as a dedicated Grants button in the main inline keyboard. Run
`/refresh` first when a newly fetched provider observation is required.

## systemd User Service

Install the binary on the server:

```sh
mkdir -p ~/.local/bin
go build -o ~/.local/bin/scriba ./cmd/scriba
~/.local/bin/scriba --version
```

Create the env file:

```sh
mkdir -p ~/.config/scriba
printf 'SCRIBA_TELEGRAM_BOT_TOKEN=%s\n' "$SCRIBA_TELEGRAM_BOT_TOKEN" > ~/.config/scriba/scriba.env
chmod 600 ~/.config/scriba/scriba.env
```

Install the service:

```sh
mkdir -p ~/.config/systemd/user
cp deploy/systemd/scriba.service ~/.config/systemd/user/scriba.service
systemctl --user daemon-reload
systemctl --user enable --now scriba.service
systemctl --user status scriba.service
```

Useful checks:

```sh
scriba server health --env prod
scriba server stats --env prod
scriba server refresh --env prod
scriba server backup --env prod
journalctl --user -u scriba.service -n 100 --no-pager
```

Backups are online SQLite snapshots of server state only. They never include the
Scriba config or Codex/Telegram authentication. The default destination is a
`backups` directory beside `server.sqlite`; the default retention is the newest
14 Scriba backup files. Each candidate must pass SQLite `quick_check` and schema
inspection before it is promoted with owner-only permissions.

Manual restore drill (stop the service first, and never overwrite the live file):

```sh
systemctl --user stop scriba.service
cp ~/.local/state/scriba/backups/<backup>.sqlite /tmp/scriba-restore-drill.sqlite
sqlite3 -readonly /tmp/scriba-restore-drill.sqlite 'PRAGMA quick_check; SELECT max(version) FROM schema_migrations;'
scriba server status --state-path /tmp/scriba-restore-drill.sqlite
rm /tmp/scriba-restore-drill.sqlite
systemctl --user start scriba.service
```

The status command currently opens and migrates its target, so use it only on
the disposable drill copy. Automated destructive restore is intentionally not
implemented.

## Notifications

The resident server can send:

- weekly reset notifications
- low limit warnings
- reset-grant expiry warnings at 5 days, 3 days, and 1 day before expiry
- Codex Radar probability milestone alerts
- service recovery and health warnings

Reset-grant warnings are tracked per grant id and expiry timestamp. If multiple
grants are available, each grant has its own expiry schedule and dedupe key.

## Auth Requirements

Codex limit polling needs local Codex OAuth at
`${CODEX_HOME:-~/.codex}/auth.json`. An OpenAI API key cannot expose ChatGPT
subscription limits or reset grants.
