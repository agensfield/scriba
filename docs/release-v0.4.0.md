# Scriba v0.4.0 Release

Date: 2026-09-10

`v0.4.0` replaces user-facing profiles with automatically discovered accounts
and separate credential sources. Account IDs and optional aliases are stable;
login changes do not transfer history. Inactive accounts remain visible and
readable, with credential availability distinct from observation freshness.

## Product and safety

- CLI account discovery/listing/aliasing, account selectors, Telegram account
  navigation, config v3, schema 13, and account-based HTTP/SSE/MCP/delivery
  contracts replace the former profile model.
- The Codex provider activity command is now `scriba codex activity`.
- Live reads, status, activity, reset previews, and reset confirmation preserve
  one account identity through source changes and OAuth retries.
- Cold alias registration cannot overwrite a newer source binding. Fast and
  inactive-account reads never borrow another account's stored quota.
- Public auth metadata suppresses credential paths and private diagnostics,
  including direct reset plan/consume output. Reset supports `--redact`.
- Local session-log usage is not retrospectively assigned to accounts from
  auth-switch intervals. Costs are dated Standard API-equivalent estimates,
  not subscription charges or historical invoices.
- Current GPT-5.6 pricing and GPT-6 Astra are covered by reviewed provenance;
  catalog changes invalidate parsed caches. Go is pinned to 1.26.8.

## Verification and publication

- release commit/tag: `5dc56405896be7582aac7b73489c038fa5c67d85`, `v0.4.0`
- exact-commit CI: `34533375423`, all jobs passed
- release workflow: `34533773489`, passed
- published: `2026-09-10T21:52:00Z`
- release: <https://github.com/agensfield/scriba/releases/tag/v0.4.0>
- Homebrew tap: `59763fc5b3d2667e7ce84b7d9d8d89c89f964d38`

Full local normal/race/vet/static/security gates passed. Independent reviews
closed the cold-install panic, status attribution race, missing inactive limits
fallback, invalid fast-grants JSON, missing live account identity, cold-alias
timestamp race, and live CLI auth-path disclosure. Tests exercised actual
command output, synthetic provider seams, and a copy of production state.

CI `34532380149` initially failed an exact floating-point assertion on macOS
ARM. The pricing expression's fused multiply-add differs by one ULP; the test
now permits only that neighboring value and prints numeric/bit diagnostics.
Pricing was unchanged, and subsequent macOS Go/Swift CI passed.

The release workflow rebuilt every archive twice, compared the results,
verified checksums and the Linux executable, published attestations, and
verified them. The downloaded public archives were independently checksummed;
the Linux archive and checksum manifest attestations were independently
verified before deployment.

Published SHA-256 values:

| Artifact | SHA-256 |
| --- | --- |
| checksums.txt | `7146e73fce9017849f7227bf5e7b8d2f7eef5e2ebcb9fc37313d6d3d71426f2c` |
| Darwin arm64 | `63738c2b151fe605559d3b265dadf4e95b851a5f22669dd34a8fc34d9e38f5da` |
| Darwin amd64 | `9ac1515334d175e08e1b6374c2cb27014713efb8fcbc6503ddf3277f536c390b` |
| Linux amd64 | `822ec4c5c71e6c3850d3c0065434c3efb04de3976b04161bd2903c98c427e772` |
| Linux arm64 | `ed1cdf48b63e1044393ef89be926d94a5543a457337f44be5c3a859799b6560a` |

## Devbox activation

The exact public Linux amd64 binary was activated at
`2026-09-10T21:56:11Z`, PID `1577583`, with SHA-256
`db76edec1e1ea3f14ac06afef978bafe118d0a230acbadc94f80fbd35b388aa6`.
The running `/proc` executable and installed binary hashes matched.

The service was stopped before creating the authoritative schema-12 backup:

- created: `2026-09-10T21:55:38.199340750Z`
- file: `scriba-server-backup-20260910T215538.199340750Z-c01ccc09d6b8.sqlite`
- size: 68,202,496 bytes
- SHA-256: `646ac84aca19a882adf7a7576a7d4588523f37e30b9a5d9eb76f54ae44a3c869`
- schema 12; quick check OK; no retention pruning

The final copied-production migration gate passed against that stopped-service
backup before installation, preserving all 24 compared business/state
projections, including 2 accounts, 27,408 observations, 83,230 observed windows,
65 policy events/replay rows, 132 outbox rows, and 201 Telegram updates.
The old binary and byte-identical configuration were preserved privately.
Live startup migrated to schema 13 with integrity OK and zero FK violations.

Startup polling succeeded at `2026-09-10T21:56:12Z`, adding one Antari
observation while policy events remained 65 and outbox rows remained 132
delivered, with no pending/leased/dead work. Personal history retained its last
observation from September 9 at 14:15 UTC and no credential availability.
Verified account aliases `personal` and `antari` were attached without changing
login or configuration. CLI and Unix context selected personal's history while
Antari remained current. Live Antari limits and a redacted reset dry run passed;
the dry run reported three available credits and did not redeem any.
Telegram registered all 13 commands, including `accounts` and `activity`.

Normal five-minute polls succeeded at `2026-09-10T22:01:13Z` and
`2026-09-10T22:06:14Z` with zero source failures and empty queues. Observations
advanced to 27,411, while policy events remained 65 and outbox rows remained
132 delivered. The process retained PID 1577583 with zero restarts. Final
integrity and foreign-key checks passed. A post-upgrade online schema-13 backup
passed at `22:02:20Z`, SHA-256
`0c1747d4b0eef8e5b62020d0037f699806e145a4eaef04c18e1375af42c22823`.

Homebrew's genuine public 0.3.4-to-0.4.0 upgrade, formula test, strict online
audit, and linkage check passed on Linux. The installed Brew binary matched
the deployed artifact hash. Reattaching the test tap initially uninstalled its
old Brew keg; the baseline was reconstructed from canonical public history
before the actual upgrade was tested. The old keg is retained unlinked, and
the independent `.local/bin` service was untouched by Brew. No macOS Homebrew
installation test is claimed; macOS Go/Swift and cross-built archive CI passed.

Private deployment receipts and rollback artifacts remain under
`~/.local/state/scriba/deployments/release-v0.4.0.o1RYlb`.

See [schema-v13-migration.md](schema-v13-migration.md) for migration invariants
and restore-only rollback requirements.
