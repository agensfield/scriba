# Scriba Current State

Date: 2026-07-10

Scriba is the first child project under Agensfield. It is a fast, minimal
Claude Code and Codex usage tracker.

## Current Shape

- Go CLI at `cmd/scriba`.
- Implementation under `internal/`.
- Swift/AppKit/SwiftUI menu bar app under `apps/macos`, currently
  de-emphasized for mainline while the CLI/server product is polished.
- SQLite derived cache under `~/.cache/scriba` by default.
- JSON status snapshot supports `scriba status --fast`.
- Codex local reports preserve full traffic while exposing effective tokens
  (`input - cached input + output`) separately. Report totals derive from input
  plus output instead of trusting inconsistent JSONL `total_tokens` values.
- Codex reports calculate standard-tier API-equivalent cost for GPT-5.4,
  GPT-5.5, and GPT-5.6 Sol/Terra/Luna. GPT-5.6 pricing is selected per request,
  with the whole request switching to long-context rates above 272K input.
- Human reports show up to three exact model names, so secondary GPT-5.6 models
  are no longer hidden behind the dominant model. JSON keeps every model.
- Daily/weekly/monthly reports and status use the configured/system timezone;
  `--timezone` provides a per-command override and JSON records the resolved
  zone.
- The Codex parsed-event cache key is versioned with scanner semantics, so an
  upgrade cannot silently reuse incompatible historical events.
- Codex token payloads decode integers exactly beyond JavaScript's safe-integer
  boundary and reject fractional/overflow counters. Claude counters are clamped
  nonnegative without conflating cache-read tokens with uncached input.
- Both provider parser caches use explicit semantic namespaces. Frozen corpus
  fixtures, cumulative-reset properties, and native fuzzers guard parsing.
- Stable `scriba.v1` status, Codex limits, profile, and reset-grant JSON outputs,
  plus `scriba.budget.v1` reports, have checked-in Draft 2020-12 schemas and
  canonical validation goldens.
- Pricing is embedded from a hash-bound reviewed offline catalog. Maintainer
  refresh writes a candidate only; CI validates provenance, aliases, rates,
  tier thresholds, boundary goldens, and deterministic generation without
  network access.
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
- `scriba server run` is the resident devbox process for live Codex limit
  polling, reset detection, Telegram commands, limit/grant warning
  notifications, health, stats, pruning, and radar probability alerts.
- Codex limit polling reads explicit additional-rate-limit buckets, including
  Spark, and exposes the available reset-grant count plus earliest available
  grant expiry when the ChatGPT backend exposes reset-credit metadata.
- Telegram reset-grant expiry alerts are tracked per available grant credit and
  fire once at the 5-day, 3-day, and 1-day checkpoints before each credit's own
  `expires_at`.
- Telegram `/grants` and its dedicated inline keyboard button render every
  available reset grant from the latest durable server observation, including
  title, type, status, granted time, expiry, remaining lifetime, and full id.
- Schema v7 stores reset, limit-warning, grant-warning, reset-grant, and Radar
  notification intents in one canonical outbox, atomically with their typed
  business events. Telegram claims one target-filtered row at a time with a
  fenced lease; legacy delivery tables remain read-only migration evidence.
- Telegram `getUpdates` batches are durably staged as exact raw JSON before the
  polling dependency can advance its cursor. Pending updates replay after a
  crash, while malformed updates dead-letter visibly.
- Server stats and health expose outbox/inbox backlog, due work, attempts,
  oldest pending age, expired leases, and dead letters.
- `scriba codex summary` appends live Codex limit/reset-grant metadata unless
  `--no-remote` is passed.
- `scriba codex reset-grants` shows every available reset grant and each
  grant's own expiration timestamp.
- `scriba codex profile` reads the ChatGPT/Codex profile backend
  (`/backend-api/wham/profiles/me`) and renders profile token activity,
  streaks, reasoning mix, thread/skill counts, daily/weekly activity bars, and
  top skills/plugins while preserving all buckets with `--json`.
- `scriba update --check` compares the local binary to the latest GitHub tag;
  `scriba update` uses `go install` for regular installs and refuses
  Homebrew-managed binaries in favor of `brew upgrade scriba`.
- `internal/radar` reads `https://codexradar.com/current.json` first and falls
  back to the old `https://codex-reset-radar.pages.dev/current.json` endpoint.

## Current Shipping Lane

The devbox server and Telegram bot lane is implemented:

- `scriba server run` as one resident process.
- Live Codex backend limit polling from local Codex auth.
- SQLite server state.
- Weekly reset detection from `resetsAt` timestamp advances.
- Telegram long polling, commands, reset notifications, low-limit warnings,
  reset-grant expiry warnings, health/recovery alerts, stats, and radar
  probability milestone alerts.
- Radar probability alerts fire on upward 24h probability checkpoint crossings
  at 25%, 50%, and 75%; drops update the stored checkpoint silently so later
  increases can alert again.

Canonical spec:

- `/Users/arda/Documents/obsidian/obsidian-main/projects/agensfield/scriba/server-telegram-spec.md`

## Invariants

- Claude/Codex logs and provider APIs remain source of truth.
- Scriba stores only read-only derived state.
- Cache deletion is safe.
- Normal local scans read JSONL line-by-line.
- Human output is default; `--json` is explicit for agents and automation.
- `effectiveTokens` and `totalTokens` are distinct contracts: the former
  excludes cache reads, while the latter represents full model traffic.
- Calculated `costUSD` is an estimated standard API equivalent, not ChatGPT
  subscription billing.
- `--no-remote` skips provider API probes.
- `--fast` reads cached status only.
- `--redact` removes share-sensitive paths and identifiers from JSON output.
- Terminal metric rows use a shared label column per rendered provider/output so
  progress bars align across short and long labels.
- Radar alerts are derived notification state; Codex Radar JSON remains the
  source of truth.
- Server unit tests keep radar polling opt-in; the real resident server wires a
  live radar client in `scriba server run`.

## Verification

Preferred core gate:

```sh
go test ./...
go vet ./...
staticcheck ./...
golangci-lint run ./...
gosec ./...
govulncheck ./...
```

Optional macOS menu bar gate:

```sh
swift test --package-path apps/macos
apps/macos/Scripts/package_zip.sh release
```

## Open Follow-Ups

- Execute the accepted local usage control-plane program in
  [`control-plane-roadmap.md`](control-plane-roadmap.md). Reliability,
  migration safety, and the canonical outbox precede budget intelligence,
  policy, agent interfaces, profiles, and surface parity.
- Tighten Go regression tests around frozen TS-era fixtures.
- Revisit Claude `blocks` for strict bounded-memory behavior.
- Decide whether ad-hoc signed zip distribution is enough before
  notarization/Sparkle.
