# Scriba

Fast, local-first usage tracking for Claude Code and Codex.

Scriba is part of Agensfield. The alpha target is a resource-sane replacement
for the heavy `ccusage`/`openusage` path: bounded-memory local scanning, fast
incremental refresh, an agent-friendly CLI, and thin desktop/Telegram surfaces
over the same core.

## Current Alpha Shape

- `@agensfield/scriba` importable TypeScript package.
- `scriba` CLI for humans and agents.
- Tauri menu bar app as a consumer, not owner, of usage logic.
- Telegram alerts first; queryable bot commands later.
- Read-only derived cache/index. Claude/Codex logs and provider APIs remain
  source of truth.

## Docs

- [Current state](docs/current-state.md)
