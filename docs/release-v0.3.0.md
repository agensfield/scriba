# Scriba v0.3.0 Release Receipt

Scriba `v0.3.0` is the first reproducible, provenance-attested release of the
completed control-plane implementation. The annotated tag resolves to commit
`92e7147d101e76ccb691501adc7ff5d7f95f4b60`.

## Source gates

- Local `just check` passed Go tests, vet, Staticcheck, golangci-lint, Gosec,
  and govulncheck with no reachable vulnerabilities.
- CI run `29259632504` passed Go race/vet, macOS Go/Swift, and lint/security for
  the exact tagged commit.
- Release run `29259749842` passed a serial-package race suite, vet, two
  independent clean-cache builds, byte-for-byte archive comparison, checksums,
  Linux smoke, artifact attestation, consumer verification, complete draft
  assembly, and atomic publication.
- The published release is stable, non-draft, immutable, and contains exactly
  four platform archives plus `checksums.txt`.

Release re-verification deliberately uses `go test -race -p 1 ./...`. Go test
packages are separate processes, so cross-package concurrency cannot expose a
race. Serializing those processes preserves race coverage while preventing a
cold release runner from starving timing-sensitive integration goroutines.

## Published artifacts

| Artifact | SHA-256 |
| --- | --- |
| `scriba_0.3.0_darwin_amd64.tar.gz` | `f2ec4c2ab03dd8e24ca498df3d946bed077c9b50975c1baa22288841cd673116` |
| `scriba_0.3.0_darwin_arm64.tar.gz` | `df35e89fd429072c0b449f30d381af6327763d1cd57c29c33381c5629555d83b` |
| `scriba_0.3.0_linux_amd64.tar.gz` | `e72bc35103d893bb5e8d46ae0bd65ca19c3af20d10990044b557748743b1583f` |
| `scriba_0.3.0_linux_arm64.tar.gz` | `dd9a8cd72ff7b7226d2141d557513d28577aed4db6d6f8eb4fb205dec4aa6521` |

All four archives and `checksums.txt` independently passed
`gh attestation verify --repo agensfield/scriba`. A fresh pinned installer
smoke installed `v0.3.0` into a private temporary directory and reported
version `0.3.0`.

The manual Homebrew formula update is commit `154b6da` in
`agensfield/homebrew-tap`. `brew audit --strict agensfield/tap/scriba` and
`brew fetch --force agensfield/tap/scriba` passed against version `0.3.0`.

## Devbox deployment

The exact published Linux amd64 archive was copied to the private deployment
receipt directory and verified before extraction. Its installed binary SHA-256
is `6237ece07b6fcb9a281b32be800868cd31c0da7775d303973d9b9660e6bd01ee`.

Before replacement, the running `0.2.9` binary produced a verified schema-11
backup:

- path: `/home/arda/.local/state/scriba/backups/scriba-server-backup-20260713T150023.080827028Z-93478fa1a5d0.sqlite`
- SHA-256: `e2e4bab6928e57ee8cbad9a4c097aaace0443e72e2a463acbc618f2adb21a42b`
- size: 35,954,688 bytes
- `quick_check`: `ok`

The previous binary is preserved beside the release evidence. The service was
stopped, the verified candidate was installed by same-directory replacement,
and the service restarted successfully. Post-deploy evidence proves:

- version `0.3.0`, full commit
  `92e7147d101e76ccb691501adc7ff5d7f95f4b60`, schema 11;
- SQLite `quick_check=ok` and no foreign-key violations;
- server, default profile, owner-only context API, context CLI, and SSE healthy;
- outbox 44 delivered with zero pending, leased, due, expired, or dead rows;
- Telegram inbox 10 processed with zero pending, due, or dead rows after human
  health and Profiles to `default` to Limits smokes;
- service and persistent backup timer active and enabled;
- privacy-forbidden context grep and post-start journal failure-pattern scan
  clean.

Authoritative evidence is owner-only on the devbox at
`/home/arda/.local/state/scriba/deployments/release-v0.3.0-92e7147`.

The final completion audit on 2026-07-13 accepted composed two-fixture profile
isolation plus one-account live compatibility because no second authorized
Codex account is available. Durable Telegram updates `133548446` and
`133548448` through `133548450` closed the health and profile-navigation
interaction receipts without changing the immutable release artifact.
