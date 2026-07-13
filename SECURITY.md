# Security Policy

## Supported versions

Security fixes are made on `main` and released in the newest stable version.
Only the latest GitHub release is supported. Upgrade before reporting a problem
that has already been fixed on `main` or in a newer release.

## Reporting a vulnerability

Use [GitHub private vulnerability reporting](https://github.com/agensfield/scriba/security/advisories/new).
Do not open a public issue for suspected credential exposure, authentication or
authorization bypass, webhook-signing flaws, local API access, unsafe database
behavior, or release supply-chain compromise.

Include the affected version and commit, operating system, minimal reproduction,
impact, and whether credentials or user data may have been exposed. Do not send
real Codex, Telegram, webhook, or ntfy credentials. You should receive an
initial acknowledgement within seven days; remediation and disclosure timing
depend on severity and reproducibility.

## Security boundaries

Scriba is local-first, but it reads sensitive local Codex authentication and
usage data. Its server database, config, environment file, backup directory,
and Unix socket must remain owner-only. External notifications intentionally
use minimized canonical payloads; credentials remain environment-only. The
stdio MCP and owner-only Unix API are read-only agent surfaces. TCP exposure is
not enabled by the shipped context API configuration.

Verified local backups are recoverability, not disaster recovery. Operators
remain responsible for independently protected off-host copies, host access,
Telegram bot/chat configuration, webhook recipients, and ntfy topic controls.

## Release integrity

Releases produced by the reproducible workflow introduced after `v0.2.9`
contain deterministic macOS/Linux archives and `checksums.txt`. GitHub Actions
creates Sigstore-backed build provenance for those published artifacts. Verify
both before installing a downloaded archive:

```sh
asset=scriba_<version>_<os>_<arch>.tar.gz
grep "  ${asset}$" checksums.txt | sha256sum --check -
gh attestation verify "$asset" --repo agensfield/scriba
```

On macOS, replace the checksum command after the pipe with
`shasum -a 256 --check -`.

`v0.2.9` and older releases predate artifact attestations and can only be
checked against their published SHA-256 manifest. Repository-level immutable
releases prevent published assets and their associated tags from being changed
in place.

The install script verifies SHA-256 automatically. It does not currently verify
the GitHub attestation, because the GitHub CLI is not a required dependency.
