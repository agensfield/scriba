# Scriba

Fast, local-first usage tracking for Claude Code and Codex.

Scriba is part of Agensfield. The alpha target is a resource-sane replacement
for the heavy `ccusage`/`openusage` path: bounded-memory local scanning, fast
incremental refresh, an agent-friendly CLI, and thin desktop/Telegram surfaces
over the same core.

## Current Alpha Shape

- `@agensfield/scriba` importable TypeScript package.
- `scriba` CLI for humans and agents.
- Tauri menu bar app as a future consumer, not owner, of usage logic.
- Telegram alerts first; queryable bot commands later.
- Read-only derived cache/index. Claude/Codex logs and provider APIs remain
  source of truth.

## Commands

```sh
bun install
bun run gate
bun run scriba status
bun run scriba claude daily
bun run scriba claude daily --json
bun run scriba codex weekly
bun run scriba codex sessions
bun run scriba telegram alerts
bun run scriba bench ccusage --provider codex
```

Human output is the default. Use `--json` for agents and automation.

`scriba bench ccusage` is non-executing by default. Pass `--execute` only when
you intentionally want to run the bounded reference baseline.

## Docs

- [Current state](docs/current-state.md)
- [CLI](docs/cli.md)
- [Configuration](docs/config.md)
- [Benchmarks](docs/benchmarks.md)
