# Scriba v0.3.3 Release

Date: 2026-07-17

`v0.3.3` adds safe Codex reset-credit redemption to the CLI and the resident
Telegram bot.

## User-facing changes

- `scriba codex reset` fetches a fresh live plan, selects the available grant
  expiring soonest by default, and never mutates under `--dry-run`.
- CLI mutation requires an explicit confirmation or `--yes`. One transient
  retry reuses the same UUID idempotency key.
- Telegram `/reset [profile]` renders the concrete selected credit and weekly
  usage before offering Confirm and Cancel buttons. Confirmations expire after
  ten minutes and are bound to the originating chat and Telegram user.
- Cancel never consumes a grant. Duplicate Confirm callbacks cannot consume a
  second grant, and a retry reuses the original request UUID.
- Profile keyboards and the main quick-action keyboard expose the reset flow.

## Verification and publication

- CLI feature commit: `57c413f`
- Telegram feature commit: `ba669e2`
- release commit and tag:
  `a3d3d689d251e5506e868923fdaf607d19662f56`
- exact-commit CI: `29576060728`, green
- release workflow: `29576295823`, green
- release URL: <https://github.com/agensfield/scriba/releases/tag/v0.3.3>
- Homebrew tap: `d248da3`

Local `just check`, focused race suites, the exact-commit CI race/macOS/
security matrix, reproducible double-build, checksums, Linux smoke, provenance
attestations, and independent consumer verification all passed. Homebrew strict
audit, public fetch, upgrade from `0.3.2`, formula test, and installed binary
smoke passed.

Published SHA-256 values:

- checksums.txt:
  `f991f798a144e80d232c586ee76acdf1667b024ba8895dfce26eda05226e894a`
- Darwin amd64:
  `240ea8f7e70fc71af6ca0af026cac8cf17b0a6c397ce4fb73c3c66038995d35d`
- Darwin arm64:
  `e02eda962844c11776c993d6e4cd6cc2b27e4bd9858ea24c7f0432921b364569`
- Linux amd64:
  `4cd99b81486225b3e41e4cfcdf41f8d1199f0bc00f31284c9817f0545d2e4bba`
- Linux arm64:
  `a1a0134967cc5e9c772c8aa0f1eed92c33201901eb6014fd51ab99c5dd4b383e`

## Deployment

The exact public Linux amd64 archive is live on devbox. The installed binary
SHA-256 is
`88e9afa91445174d62644b22504cc04ca8e483e62c460a939dfbd5a81354e0d5`.
The verified predeploy schema-12 backup is
`scriba-server-backup-20260717T112556.544664789Z-a928d7df3f97.sqlite`,
SHA-256
`fcc692e6863fa904f2309218411c2fb9ff4372d069407512dd428a591f45b811`.
Deployment evidence and the preserved `v0.3.2` binary live under
`~/.local/state/scriba/deployments/release-v0.3.3-a3d3d68-20260717T112635Z`.

Post-deploy health is fresh and `ok` at schema 12 with zero consecutive
failures and zero service restarts. SQLite `quick_check` passed, foreign-key
checks were empty, and both durable queues were empty. Telegram registered
`/reset` in its 13-command set and completed a startup delivery. A live
deployed dry run selected the oldest credit, expiring
`2026-07-18T00:29:25.147934Z`, with weekly usage at 90 percent and all five
grants still available. No reset grant was consumed.
