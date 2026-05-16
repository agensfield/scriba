# Scriba

Fast, local-first usage tracking for Claude Code and Codex.

Scriba is part of Agensfield. It is now a native Go CLI with a Swift macOS menu
bar consumer. The core reads local Claude/Codex logs, keeps derived cache state,
probes provider usage windows when auth is available, and emits human output by
default with JSON for agents and automation.

## Current Shape

- `scriba` native Go CLI.
- Swift/AppKit/SwiftUI menu bar app in `apps/macos`.
- Read-only derived cache/index. Claude/Codex logs and provider APIs remain
  source of truth.
- macOS packaging bundles a native `Contents/Helpers/scriba` fallback binary,
  not Bun, Node, JS resources, or `node_modules`.

## Commands

```sh
go build -o .build/scriba ./cmd/scriba
go test ./...
go vet ./...
staticcheck ./...
golangci-lint run ./...
gosec ./...
govulncheck ./...

go run ./cmd/scriba doctor
go run ./cmd/scriba status
go run ./cmd/scriba status --fast --json
go run ./cmd/scriba claude daily
go run ./cmd/scriba codex weekly
go run ./cmd/scriba cache status
go run ./cmd/scriba telegram alerts

swift test --package-path apps/macos
apps/macos/Scripts/compile_and_run.sh --test --open-menu
apps/macos/Scripts/package_zip.sh release
```

Human output is the default. Use `--json` for agents and automation.

The root `justfile` wraps the common local loops:

```sh
just
just check
just macos-start
just macos-package
just macos-launch
just macos-release
```

## Docs

- [Current state](docs/current-state.md)
- [CLI](docs/cli.md)
- [Configuration](docs/config.md)
- [Benchmarks](docs/benchmarks.md)
