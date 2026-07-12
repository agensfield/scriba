# Scriba Configuration

Scriba runs with defaults, reads `~/.config/scriba/config.json` when it exists,
and accepts an explicit JSON config via `--config`.

```json
{
  "schemaVersion": 2,
  "defaultProfileId": "default",
  "profiles": [
    {
      "id": "default",
      "label": "Personal",
      "enabled": true,
      "codexAuthPaths": ["/Users/arda/.codex/auth.json"]
    }
  ],
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
  "server": {
    "enabled": false,
    "statePath": "/Users/arda/.local/state/scriba/server.sqlite",
    "contextAPI": {
      "enabled": false,
      "socketPath": "/Users/arda/.local/state/scriba/context.sock"
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

Config v2 establishes explicit profile identity and Codex auth routing. Profile
IDs are stable lowercase slugs up to 32 characters; one enabled profile must be
the default, and every enabled profile needs at least one absolute auth-file
path. Cleaned auth paths cannot be duplicated across profiles. Scriba never
persists these paths into server SQLite or exposes them through public JSON.

Existing schema-v1 files continue to load without being rewritten. Scriba
normalizes them in memory to one implicit `default` profile using the existing
`server.accountLabel` and legacy Codex auth discovery. Explicit schema-v2 files
never fall back to ambient `CODEX_HOME` discovery. Multi-profile resident
polling and public selectors land in the following Wave 3.2 slices; this commit
only freezes config and request-isolation contracts.

`server.contextAPI` is opt-in. When enabled without `socketPath`, Scriba places
`context.sock` beside the resolved server database. An explicit socket path
must be absolute. Scriba creates a trusted owner-only parent (`0700`) and a
same-user Unix socket (`0600`); TCP is not enabled by this setting. Start the
resident process with `scriba server run` and query it with, for example,
`curl --unix-socket ~/.local/state/scriba/context.sock http://localhost/v1/health`.

Scriba temporarily enables compatibility with OpenAI's July 2026 removal of
the Codex five-hour bucket. When the backend returns a lone seven-day
`primary_window`, Scriba reports it as weekly instead of fabricating a five-hour
window. Set `SCRIBA_FEATURE_CODEX_TEMPORARY_NO_FIVE_HOUR=false` to disable this
interpretation immediately if the upstream experiment changes unexpectedly.
Normal primary-five-hour plus secondary-weekly responses are unaffected.

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
