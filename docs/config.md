# Scriba Configuration

Scriba runs with defaults, reads `~/.config/scriba/config.json` when it exists,
and accepts an explicit JSON config via `--config`.

```json
{
  "schemaVersion": 1,
  "cacheDir": "/Users/arda/.cache/scriba",
  "timezone": "Europe/Istanbul",
  "providers": {
    "claude": {
      "enabled": true,
      "paths": []
    },
    "codex": {
      "enabled": true,
      "paths": []
    }
  },
  "telegram": {
    "enabled": false,
    "botToken": "",
    "botTokenEnv": "SCRIBA_TELEGRAM_BOT_TOKEN",
    "chatId": "",
    "alerts": {
      "sessionPercent": 80,
      "weeklyPercent": 80,
      "includeErrors": true
    }
  }
}
```

Configure Telegram from the CLI:

```sh
scriba config path
scriba config init
scriba config telegram --enable --chat-id "$TELEGRAM_CHAT_ID" --bot-token-env SCRIBA_TELEGRAM_BOT_TOKEN
scriba config telegram --enable --chat-id "$TELEGRAM_CHAT_ID" --bot-token "$TELEGRAM_BOT_TOKEN"
scriba config telegram --session-percent 80 --weekly-percent 80 --include-errors
scriba telegram alerts --json
scriba telegram alerts --send
```

`telegram.botToken` is optional and is stored in the local config file with
`0600` permissions. `telegram.botTokenEnv` remains supported for terminal
workflows that prefer environment-owned secrets.

Empty provider `paths` use defaults:

- Claude: `~/.config/claude/projects`, `~/.claude/projects`
- Codex: `${CODEX_HOME:-~/.codex}/sessions`

`timezone` is optional. When omitted, calendar reports and status use the
system timezone. Set an IANA name such as `Europe/Istanbul` to make grouping
portable across machines, or override it per command with `--timezone`.
