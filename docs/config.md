# Scriba Configuration

Scriba discovers config from the first readable file in this order:

1. `--config <path>`
2. `SCRIBA_CONFIG`
3. `./scriba.config.json`
4. `./scriba.config.jsonc`
5. `~/.config/scriba/config.json`
6. `~/.scriba/config.json`

The schema is versioned with `schemaVersion: 1`. Print the current JSON schema
metadata with:

```sh
bun run scriba schema
```

## Minimal Example

```json
{
  "schemaVersion": 1,
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
    "botTokenEnv": "SCRIBA_TELEGRAM_BOT_TOKEN",
    "alerts": {
      "sessionPercent": 80,
      "weeklyPercent": 80,
      "includeErrors": true
    }
  }
}
```

Empty provider `paths` means Scriba uses provider defaults:

- Claude: `$CLAUDE_CONFIG_DIR/projects`, or `~/.config/claude/projects` and
  `~/.claude/projects`.
- Codex: `${CODEX_HOME:-~/.codex}/sessions`.

`cacheDir` defaults to the user cache directory. The cache is derived state only
and can be rebuilt from local logs plus provider APIs. `scriba cache status`
shows cache size, schema version, WAL state, snapshots, scan stats, and cached
file-event counts. `scriba cache prune` removes file-event rows for deleted log
files, and `scriba cache vacuum` compacts the SQLite file after truncating WAL
state and waiting for settled filesystem sizes. Vacuum reports signed
`deltaBytes`, plus `reclaimedBytes` or `grewBytes`, because SQLite can
occasionally rebuild into a slightly larger file.
