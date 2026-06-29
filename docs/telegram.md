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
/profile
/refresh
/stats
/lastreset
```

`/profile` fetches the ChatGPT/Codex profile stats backend on demand and
renders token activity, streaks, reasoning mix, and top skills/plugins in a
compact Telegram card.

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
journalctl --user -u scriba.service -n 100 --no-pager
```

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
