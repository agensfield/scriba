# Scriba v0.3.2 Release

Date: 2026-07-14

`v0.3.2` fixes a production health flap caused by small upstream reset-clock
changes inside one Codex quota cycle.

## Incident and root cause

After `v0.3.1`, the Codex backend reported the same weekly quota cycle with
reset timestamps that moved between `2026-07-19T20:15:44Z`, `20:15:45Z`, and
`20:15:46Z`. Pacing-warning identity used the exact reset timestamp. Each
forward tick therefore looked like a new cycle and created another durable
warning. When the timestamp moved back to an already-seen value, its event ID
matched the older warning while its payload reflected the newer pacing state.
The semantic-duplicate guard correctly rejected that conflict, but the whole
poll transaction rolled back and health exposed `internal/persistence_failed`.

Between the `v0.3.1` deployment and this fix, the resident process recorded 150
failed polls and 73 successful observations. It did not crash or restart. This
was not an OpenAI endpoint outage: the quota endpoint kept returning valid
data, but harmless reset-clock jitter violated Scriba's event-identity
assumption. Separate Telegram `getUpdates` 502s and timeouts occurred during
the window and recovered independently.

## Fix

- Pacing persistence now derives a stable cycle reset using the same monotonic
  clock-jitter tolerance as reset detection. Budget calculations still use the
  newest raw provider reset.
- The stable reset owns pacing state, event identity, and stored warning
  payloads, so second-level drift cannot create or conflict with another event
  in the same cycle.
- Compatibility coverage reproduces both ordinary forward/backward jitter and
  the exact deployed dirty state: an event at `20:15:45Z`, state at
  `20:15:46Z`, then a provider response returning to `20:15:45Z`.
- Profile poll failures now log sanitized profile, stage, failure-kind, and
  failure-code fields before durable health aggregation.

## Verification and publication

- fix commit: `3b340e8`
- release commit and tag: `c5cd5b2050c2976194616f36a9c16a2a5facc97f`
- exact-commit CI: `29332564726`, green
- release workflow: `29332840366`, green
- focused race suites and `go test -race -p 1 -count=1 ./...`: green
- local `just check`: green, including staticcheck, golangci-lint, Gosec, and
  Govulncheck
- release URL: <https://github.com/agensfield/scriba/releases/tag/v0.3.2>
- Homebrew tap: `5bb1171`

The release workflow built every archive twice, compared the trees, verified
checksums, smoked the Linux executable, and published provenance attestations.
Independent archive downloads matched the published checksums. Homebrew audit,
fetch, upgrade, and formula test passed.

Published SHA-256 values:

- checksums.txt: `5baca827a972c32c94cf9a3bb3ed500a17b4970ffecf9af7c6cc7bb1e1a66afc`
- Darwin amd64: `0ec788c860ecedebbca68c998af25d767b42358896fa1c7af257a9f40712f05c`
- Darwin arm64: `98234654e1b3b7df85a4536c8fde0e51c4ee6cb1b919a659c3d430739a5237b5`
- Linux amd64: `11111a74350f205c9636503571bc193bbfaefbfe9cad04c7ac47726727716677`
- Linux arm64: `a1e5b588941b433c323fee70dcaaaef4a2f75029b343981e402202b42751dac2`

## Deployment

The exact public Linux amd64 artifact is live on devbox. The installed binary
SHA-256 is
`be7f7e00850c29bae8d34e893077fd731d88b69b4bcff23af84e01fb2af9137c`.
The verified predeploy schema-12 backup is
`scriba-server-backup-20260714T123238.408092743Z-5cbaf2fb093c.sqlite`, SHA-256
`48f46dd3c6352cb1a514d57b7d92e105e936ae87b0ad5c32b8fbede601718cbe`.
Deployment evidence and the preserved `v0.3.1` binary live under
`~/.local/state/scriba/deployments/release-v0.3.2-c5cd5b2-20260714T124224Z`.

The startup poll and two explicit refreshes accepted the previously failing
shape: raw weekly reset `20:15:45Z` while durable pacing state remained at
`20:15:46Z`. Pacing event/outbox counts remained `3/3`, queues were empty,
health recovered to `ok`, and the service had zero restarts. The first
untouched resident poll then succeeded at `2026-07-14T12:47:41Z`; event/outbox
counts still remained `3/3`, `quick_check` passed, foreign-key checks were
empty, queues stayed empty, and the post-deploy journal had no failure-pattern
hits.
