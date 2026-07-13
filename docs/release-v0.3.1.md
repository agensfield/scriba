# Scriba v0.3.1 Release

Date: 2026-07-14

`v0.3.1` makes budget output readable by humans and adds durable, quiet Codex
pacing warnings.

## User-facing changes

- Human `codex budget` and `claude budget` output explains whether usage is on
  track, current versus sustainable pace, projected exhaustion, and how far it
  precedes reset. Raw diagnostic reason codes remain in the unchanged JSON
  contract.
- The resident server sends one proactive warning per primary five-hour or
  weekly reset window when risk is high and more than 20 percent remains.
- Critical pacing does not send another warning. Existing 20/10/5/0-percent
  remaining checkpoints own late-cycle alerts.
- Pacing events and Telegram, webhook, or ntfy intents are persisted atomically
  through the canonical outbox. Schema v12 preserves dedupe across refreshes,
  retries, and restarts.

## Verification and publication

- release commit: `9276bd3d81477237704266043504e5cf4bea497b`
- exact-commit CI: `29284179907`, green
- local `just check`: green, including staticcheck, golangci-lint, Gosec, and
  Govulncheck
- local uncached full race suite: green
- release URL: <https://github.com/agensfield/scriba/releases/tag/v0.3.1>
- Homebrew tap: `7773439`

The release workflow built every archive twice, matched the two trees, verified
checksums, smoked the Linux executable, created one provenance attestation for
all five assets, verified each attestation, and uploaded the complete draft.
GitHub's release-list API did not expose the just-created draft quickly enough
for the final lookup, so run `29284431591` failed only at publication.

The known draft release ID was then independently checked for its exact tag and
five asset names. Every archive checksum and all five Sigstore attestations
were reverified before that same draft was published. Commit `623d3fd` now uses
the release action's direct `id` output instead of an eventually consistent
list query; CI `29285107517` is green.

Published archive SHA-256 values:

- Darwin amd64: `8758c8fd17e0816abe56c2f6e8ac3d44838d7a12c30b221407b9b68c8b3fb7d5`
- Darwin arm64: `f48f7905bd50078697c9d4931659458f668498fd1e091a75713f805dc809a4b7`
- Linux amd64: `c6f57c9a345c0888d50a1321be658876a79bbdd14780d08a084590c54c377d3e`
- Linux arm64: `3afb1214cb6122b96c287e4343c6d6a9ae76db953560559b40afc5e777a8258e`

## Deployment

The exact published Linux amd64 archive is live on devbox as `v0.3.1`, commit
`9276bd3`, schema 12. The stopped-service migration, rollback, integrity,
single-delivery, refresh, and restart receipts are in
[`schema-v12-migration.md`](schema-v12-migration.md).
