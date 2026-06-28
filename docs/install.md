# Install

Scriba is distributed as one Go binary named `scriba`.

## Go Install

Requires Go 1.26 or newer:

```sh
go install github.com/agensfield/scriba/cmd/scriba@latest
scriba --version
scriba doctor
```

For a pinned release:

```sh
go install github.com/agensfield/scriba/cmd/scriba@v0.2.0
```

## From Source

```sh
git clone https://github.com/agensfield/scriba.git
cd scriba
go build -o .build/scriba ./cmd/scriba
.build/scriba doctor
```

Local development helpers:

```sh
just check
just install-cli
just cli status
```

## GitHub Binaries

Release builds are published on GitHub releases as compressed binaries for the
main macOS and Linux targets. Download the archive for your platform, place the
`scriba` binary on `PATH`, then verify it:

```sh
scriba --version
scriba doctor
```

## Homebrew

The intended Homebrew path is:

```sh
brew install agensfield/tap/scriba
```

Use `go install` or GitHub release binaries until the tap formula is published
for the release you want.

## Updates

Check the latest tagged release:

```sh
scriba update --check
scriba update --check --json
```

For regular Go installs, `scriba update` installs the latest tagged release
with:

```sh
go install github.com/agensfield/scriba/cmd/scriba@<latest-tag>
```

For Homebrew installs, self-update is intentionally disabled. Use:

```sh
brew upgrade scriba
```

Scriba detects Homebrew by resolving its executable path and checking whether it
lives under a Homebrew `Cellar/scriba` path.

## Auth Paths

Scriba reads local app auth and logs. It does not use OpenAI API keys for
ChatGPT/Codex subscription windows.

- Claude logs: `~/.config/claude/projects`, `~/.claude/projects`
- Codex logs: `${CODEX_HOME:-~/.codex}/sessions`
- Codex OAuth: `${CODEX_HOME:-~/.codex}/auth.json`
- Scriba config: `~/.config/scriba/config.json`
- Scriba cache: `${XDG_CACHE_HOME:-~/.cache}/scriba`
- Scriba server state: `${XDG_STATE_HOME:-~/.local/state}/scriba`
