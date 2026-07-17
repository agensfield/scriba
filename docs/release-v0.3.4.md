# Scriba v0.3.4 Release

Date: 2026-07-17

`v0.3.4` fixes a production health degradation caused by second-level reset
clock jitter in ordinary remaining-limit warnings.

## Incident and root cause

The Codex backend reported one weekly quota cycle as
`2026-07-23T04:15:55Z`, then `04:15:57Z`, then `04:15:55Z` again. Scriba's
remaining-checkpoint policy used the raw reset timestamp for cycle state,
semantic event identity, and payloads. The forward tick emitted a duplicate
5-percent-remaining warning. When the clock moved back, the event ID matched
the first warning while its payload and detection time differed. The immutable
semantic-conflict guard correctly rejected the conflict, but that rolled back
every subsequent poll as `internal/persistence_failed`.

The upstream quota endpoint remained healthy and returned fresh usage and
grant data throughout. The resident process did not crash or restart, and
Telegram ingress remained functional. This was Scriba treating harmless
provider clock jitter as distinct quota cycles, not an OpenAI endpoint outage.

## Fix

- Remaining-checkpoint policy now derives a stable cycle reset within the same
  five-minute jitter tolerance used by reset-transition policy.
- The stable reset owns checkpoint state, event identity, and stored payloads;
  the raw reset remains recorded as the latest provider observation.
- Existing deployed state with no stable reset repairs itself without clearing
  already-reached checkpoints or emitting another warning.
- Regression coverage reproduces the exact production `:55 -> :57 -> :55`
  sequence through both the policy evaluator and the atomic SQLite poll path.

## Verification and publication

- fix commit: `f843f41990f3002c4a81cad5e5a21395c3e1092c`
- release commit and tag: `2b8fcd483c47b34dbbde44aa0cc39b01200f724c`
- fix CI: `29589508707`, green
- exact release-commit CI: `29589814010`, green
- release workflow: `29590096138`, green
- release URL: <https://github.com/agensfield/scriba/releases/tag/v0.3.4>
- Homebrew tap: `eef07c0`

Focused race suites and local `just check` passed, including all tests, vet,
staticcheck, golangci-lint, Gosec, and Govulncheck. The release workflow built
every archive twice, verified checksums and Linux smoke, published provenance
attestations, and independently verified them. Homebrew strict audit, public
fetch, upgrade from `0.3.3`, and formula test passed.

Published SHA-256 values:

- checksums.txt:
  `6f54b707bd0fcc274c8c7cd1a0be673df7214b0570f835477138280a95aae2b0`
- Darwin amd64:
  `44550963cfc012892b763be6d309b5e5afae6657350cccf530abe21a0da2dfe2`
- Darwin arm64:
  `4a85b045f455944c020e9bb9e12b9572b018d0305248ec867e283f6b75cf9a5a`
- Linux amd64:
  `c9fa472620fd19aaec3d1a7e93577a5e46cb3c4c58914fb951834b74c706d3a7`
- Linux arm64:
  `0031cff23d578e86834ded298078d589b2557f96896d85479b0099e5b108d3af`

## Deployment

The exact public Linux amd64 artifact is live on devbox. The installed binary
SHA-256 is
`07a84665015979ffec990567f433d80b8b5fe546e01b981d59310c177f5fdb38`.
The verified predeploy schema-12 backup is
`scriba-server-backup-20260717T150700.715275172Z-1ba068e67b75.sqlite`, SHA-256
`677e886f6bb6ee1e9cbd8e91888964de2449b22cb1693be3a15f75f61891de7a`.
Deployment evidence and the preserved `v0.3.3` binary live under
`~/.local/state/scriba/deployments/release-v0.3.4-2b8fcd4-20260717T1507Z`.

A candidate refresh against an exact production backup copy first repaired
health from 14 consecutive failures to `ok`, added one observation, preserved
all warning/event/outbox counts, and passed SQLite integrity checks with
notification delivery disabled. After live cutover, the startup and explicit
refresh both succeeded, health returned to `ok` with zero consecutive failures,
the service had zero restarts, all queues were empty, warning counts remained
unchanged, and SQLite `quick_check` plus foreign-key checks passed. An untouched
resident poll then succeeded at `2026-07-17T15:12:33Z`: observations advanced
to 12,557 while typed warnings remained 19, policy warnings remained 5, and
the delivered outbox remained 58 with no pending or dead rows.

Weekly usage was 98 percent at cutover, so the standing 99-percent automatic
reset rule did not consume a credit. All five reset grants remained available.
