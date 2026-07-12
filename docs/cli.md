# Scriba CLI

The CLI is the contract for both humans and agents. Human-friendly output is
the default; pass `--json` for machine-readable output.

## Root

```sh
scriba
scriba --version
scriba doctor
scriba doctor --no-remote --json
scriba status
scriba status --fast
scriba status --no-remote
scriba status --redact --json
scriba schema
scriba config path
scriba config show --json
scriba config init
scriba config telegram --enable --chat-id <id> --bot-token-env SCRIBA_TELEGRAM_BOT_TOKEN
scriba cache status
scriba cache reset
scriba cache prune
scriba cache vacuum
scriba update --check
scriba update
scriba telegram alerts
scriba telegram alerts --refresh
scriba telegram reset --send --provider codex --label weekly --message "🎉 Tibo just reset limits! 🎊"
```

Bare `scriba` is equivalent to `scriba status`.

`status` composes local log summaries and remote provider-window probes when
auth is available. It writes a derived JSON snapshot and SQLite scan stats
unless `--no-cache` is passed.

`doctor` checks local source directories, auth paths, remote reachability,
cache schema/WAL state, cache size, and latest snapshot age. It reports `ok`,
`degraded`, or `broken`.

`--fast` reads the cached status snapshot only. It is intended for the menu bar,
Telegram, and agent reads that should not trigger a foreground scan.

## Config

`scriba config telegram` edits the same config file used by the macOS menu bar
settings window. Bot tokens can be stored in the config for app use, or supplied
through `telegram.botTokenEnv` for terminal-only workflows.

`scriba telegram alerts` reads the cached status snapshot by default so menubar
actions stay fast. Pass `--refresh` when you explicitly want it to rebuild status
before evaluating alerts.

`scriba telegram reset` is the small one-shot send path used by ScribaBar when a
reset heuristic fires. It reads the same Telegram config and only sends when
Telegram is enabled.

## Reports

```sh
scriba claude summary
scriba claude daily
scriba claude weekly
scriba claude monthly
scriba claude sessions
scriba claude blocks
scriba claude budget

scriba codex summary
scriba codex daily
scriba codex weekly
scriba codex monthly
scriba codex sessions
scriba codex limits
scriba codex reset-grants
scriba codex profile
scriba codex budget
scriba codex limits --fast
```

Report commands support `--since` and `--until`, accepting full timestamps or
`YYYY-MM-DD` dates. Calendar reports use the system timezone by default. Set
`timezone` in the config or pass an IANA name with `--timezone`, for example
`--timezone Europe/Istanbul`. JSON report payloads record the resolved
timezone.

Codex local reports expose two intentionally different totals:

- `effectiveTokens` is uncached input plus output. It matches the accounting
  shape used by Codex goals, subject to the goal's own active time window.
- `totalTokens` is full model traffic: input (including cached input) plus
  output.

`inputTokens` preserves the raw Codex input counter, including cache reads;
`uncachedInputTokens` and `cachedInputTokens` split it explicitly. Reasoning
tokens are already included in output and are not counted again. Human output
leads with effective tokens, keeps traffic/cache/output visible, and lists up
to three materially used exact model names instead of hiding every model except
the dominant one.

Known Codex models receive a calculated `costUSD`. The human label is `est.`
because this is a standard-tier API-equivalent estimate, not a claim about
what a ChatGPT subscription was charged. GPT-5.6 Sol, Terra, and Luna use
OpenAI's short-context input/cache/output prices and per-request long-context
rates above 272K input tokens. The whole request switches tiers; reasoning is
not billed separately. Unknown models retain `costUSD: null` and
`pricingState: "missing"`.

`scriba codex limits` skips local log scanning and only fetches Codex usage
windows from the logged-in ChatGPT/Codex backend. It reads Codex OAuth state
from `${CODEX_HOME:-~/.codex}/auth.json`; OpenAI API key auth cannot expose
these ChatGPT subscription windows. The live payload includes primary Codex
windows, explicit additional model windows such as Spark, and the available
rate-limit reset grant count. When the read-only reset-credit metadata endpoint
answers, Scriba also shows the earliest available grant expiry. Pass `--fast`
to read the last cached `scriba status` snapshot instead of making a network
request.

`scriba codex summary` keeps the local usage summary and, unless `--no-remote`
is passed, appends the same live Codex limits and reset-grant metadata.

`scriba codex reset-grants` shows available rate-limit reset grants as a focused
view, including every available grant's `grantedAt` and `expiresAt` timestamp
when OpenAI exposes the read-only reset-credit metadata. The short alias is
`scriba codex grants`.

`scriba codex profile` shows the ChatGPT/Codex profile token-activity backend:
lifetime and peak tokens, streaks, longest turn duration, reasoning/fast-mode
mix, thread and skill counts, daily/weekly activity bars, and top skills/plugins.
`--json` preserves the full daily, weekly, and cumulative daily bucket arrays
for agents. The backend currently reports complete generated buckets, so the
current day may be absent until OpenAI generates the next profile snapshot.
These provider-generated buckets have no model attribution and are independent
from local rollout accounting, timezone grouping, and API-equivalent cost
estimates; do not expect exact day-by-day reconciliation.

`scriba codex budget` and `scriba claude budget` fetch current provider quota
windows and derive pacing, safe allowance, projected exhaustion, risk,
freshness, confidence, and explicit reason codes. Budget quantities are quota
percentage points, not local token counts: `3pp/h` means three points of the
provider-reported quota percentage per hour. These commands deliberately do
not accept `--fast`; a budget must start from a fresh provider observation.

Codex can use matching durable server observations from the preceding 24 hours
for its recent-burn estimate; samples less than 10 minutes apart are ignored.
Claude currently reports honest current-cycle confidence because it has no
durable quota-window history. Derived budget values are never persisted. The
machine-readable contract is `scriba.budget.v1`, validated by
[`schemas/budget.schema.json`](../schemas/budget.schema.json). See
[`budget-and-policy.md`](budget-and-policy.md) for the full checkpoint and its
current runtime boundary.

The resident server stores every available reset credit from the read-only
metadata endpoint when present. Telegram grant-expiry warnings are deduped by
grant id, expiry timestamp, and checkpoint, and fire once at 5 days, 3 days,
and 1 day before that grant's own expiry.

Human metric output aligns labels within each rendered provider/output, so
progress bars start in the same column even when labels differ in length.

## Updates

`scriba update --check` compares the current binary to the latest GitHub tag and
prints the detected install manager/path. `scriba update` installs the latest
tag with:

```sh
go install github.com/agensfield/scriba/cmd/scriba@<latest-tag>
```

When the binary resolves under a Homebrew `Cellar/scriba` path, Scriba refuses
self-update and points the user to `brew upgrade scriba`.

## Package Execution

Scriba is a Go binary. Build locally with:

```sh
go build -o .build/scriba ./cmd/scriba
```

The macOS app bundles this native binary directly at
`ScribaBar.app/Contents/Helpers/scriba`.
