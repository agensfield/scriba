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
    "observationRetentionDays": 120,
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
  },
  "deliveries": {
    "webhooks": [
      {
        "id": "deploy",
        "enabled": true,
        "url": "https://example.com/scriba/events",
        "secretEnv": "SCRIBA_WEBHOOK_DEPLOY_SECRET"
      }
    ],
    "ntfy": [
      {
        "id": "phone",
        "enabled": true,
        "url": "https://ntfy.sh",
        "topic": "scriba_private_random_topic",
        "tokenEnv": "SCRIBA_NTFY_TOKEN"
      }
    ]
  }
}
```

`server.observationRetentionDays` is the resident database history horizon and
defaults to 120 days (accepted range 1-36500). The daily prune removes old
observations, typed and policy events, superseded replay tombstones, terminal
canonical and legacy deliveries, and processed/dead Telegram inbox rows.
Pending or leased work is never
pruned. Policy replay keeps its monotonic high-water mark plus one explicit
per-account prune floor, so clients with a cursor older than retained history
receive `cursor_expired` instead of a silent gap even though replay sequences
are global across accounts. `scriba server prune` runs the same transaction on
demand and then checkpoints and vacuums SQLite when rows changed.

Config v2 establishes explicit profile identity and Codex auth routing. Profile
IDs are stable lowercase slugs up to 32 characters; one enabled profile must be
the default, and every enabled profile needs at least one absolute auth-file
path. Cleaned auth paths cannot be duplicated across profiles. Scriba never
persists these paths into server SQLite or exposes them through public JSON.

Existing schema-v1 files continue to load without being rewritten. Scriba
normalizes them in memory to one implicit `default` profile using the existing
`server.accountLabel` and legacy Codex auth discovery. Explicit schema-v2 files
never fall back to ambient `CODEX_HOME` discovery. Multi-profile resident
polling and public selectors are active across CLI, Unix HTTP/SSE, MCP, and
Telegram.

Enabled webhook and ntfy deliveries receive their own stable outbox targets,
`webhook:<id>` and `ntfy:<id>`. IDs are lowercase slugs up to 32 characters.
Secrets are environment-only: webhooks require `secretEnv`; ntfy accepts an
optional `tokenEnv` for protected topics. Scriba never persists either value.
Ntfy topics follow the upstream 1-64 character letter/number/underscore/dash
grammar. On public ntfy instances, an unprotected topic effectively acts as a
password, so use a random unguessable name or server-side access controls.

Webhooks receive byte-stable `scriba.notification.v1` JSON. Each request has
`X-Scriba-Event-ID`, a Unix `X-Scriba-Timestamp`, and
`X-Scriba-Signature: v1=<hex>` where the HMAC-SHA256 input is exactly
`<timestamp>.<request-body>`. Redirects are never followed. Ntfy uses its root
JSON publish endpoint with the same canonical envelope as the message body.
Both adapters use deterministic retry/terminal HTTP classes and cap
`Retry-After` at one hour without shortening Scriba's own backoff.

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
