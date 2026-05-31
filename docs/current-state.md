# Scriba Current State

Date: 2026-05-16

Scriba is the first child project under Agensfield. It is a fast, minimal
Claude Code and Codex usage tracker.

## Current Shape

- Go CLI at `cmd/scriba`.
- Implementation under `internal/`.
- Swift/AppKit/SwiftUI menu bar app under `apps/macos`.
- SQLite derived cache under `~/.cache/scriba` by default.
- JSON status snapshot supports `scriba status --fast`.
- The macOS app resolves a system `scriba` when same/newer than the bundled
  helper, otherwise it uses the bundled native Go helper.
- The macOS app exposes used/remaining display mode, menu bar text mode, and
  refresh cadence settings.
- Telegram alert config is shared between `scriba config telegram` and the
  macOS settings window.
- The app uses controlled native menu rows and macOS 26 glass button styles in
  the settings window when available.
- Packaging supports host-arch and universal app/helper builds. Debug builds use
  a separate bundle identifier from release builds.

## Current Shipping Lane

The next shippable lane is the devbox server and Telegram bot:

- `scriba server run` as one resident process.
- Live Codex backend limit polling from local Codex auth.
- SQLite server state.
- Weekly reset detection from `resetsAt` timestamp advances.
- Telegram long polling, commands, and reset notifications.

Canonical spec:

- `/Users/arda/Documents/obsidian/obsidian-main/projects/agensfield/scriba/server-telegram-spec.md`

## Invariants

- Claude/Codex logs and provider APIs remain source of truth.
- Scriba stores only read-only derived state.
- Cache deletion is safe.
- Normal local scans read JSONL line-by-line.
- Human output is default; `--json` is explicit for agents and automation.
- `--no-remote` skips provider API probes.
- `--fast` reads cached status only.
- `--redact` removes share-sensitive paths and identifiers from JSON output.

## Verification

Preferred local gate:

```sh
go test ./...
go vet ./...
staticcheck ./...
golangci-lint run ./...
gosec ./...
govulncheck ./...
swift test --package-path apps/macos
apps/macos/Scripts/package_zip.sh release
```

## Open Follow-Ups

- Tighten Go regression tests around frozen TS-era fixtures.
- Revisit Claude `blocks` for strict bounded-memory behavior.
- Decide whether ad-hoc signed zip distribution is enough before
  notarization/Sparkle.
