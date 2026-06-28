set shell := ["bash", "-eu", "-o", "pipefail", "-c"]

alias b := build
alias t := test
alias g := check
alias m := macos-start
alias mp := macos-package
alias ml := macos-launch
alias mr := macos-release
alias ic := install-cli
alias mi := macos-install
alias mu := macos-use
alias i := install
alias u := use

default:
    @just --list

build:
    go build -o .build/scriba ./cmd/scriba

install: install-cli macos-install

use: install-cli macos-use

install-cli bin="":
    @target="{{bin}}"; \
    if [[ -z "$target" ]]; then \
      gobin="$(go env GOBIN)"; \
      if [[ -z "$gobin" ]]; then gobin="$(go env GOPATH)/bin"; fi; \
      target="$gobin/scriba"; \
    fi; \
    mkdir -p "$(dirname "$target")"; \
    go build -o "$target" ./cmd/scriba; \
    "$target" --version; \
    "$target" config path

cli *args:
    go run ./cmd/scriba {{args}}

doctor:
    go run ./cmd/scriba doctor

status:
    go run ./cmd/scriba status

status-json:
    go run ./cmd/scriba status --fast --json

test:
    go test ./...

lint:
    go vet ./...
    staticcheck ./...
    golangci-lint run ./...
    gosec ./...
    govulncheck ./...

check: test lint

check-all: check macos-test

gate: check

macos-test:
    swift test --package-path apps/macos

macos-start:
    apps/macos/Scripts/compile_and_run.sh --test --open-menu

macos-package:
    apps/macos/Scripts/package_app.sh debug

macos-launch:
    SCRIBABAR_OPEN_MENU=1 apps/macos/Scripts/launch.sh

macos-preview: macos-package macos-launch

macos-install app_dir="":
    @install_dir="{{app_dir}}"; \
    if [[ -z "$install_dir" ]]; then install_dir="$HOME/Applications"; fi; \
    just macos-package; \
    src="apps/macos/.build/package/ScribaBar.app"; \
    dest="$install_dir/ScribaBar.app"; \
    mkdir -p "$install_dir"; \
    osascript -e 'tell application "ScribaBar" to quit' >/dev/null 2>&1 || true; \
    if [[ -e "$dest" ]]; then \
      if command -v trash >/dev/null 2>&1; then trash "$dest"; else rm -rf "$dest"; fi; \
    fi; \
    ditto "$src" "$dest"; \
    echo "$dest"

macos-open-installed app_dir="":
    @install_dir="{{app_dir}}"; \
    if [[ -z "$install_dir" ]]; then install_dir="$HOME/Applications"; fi; \
    SCRIBABAR_OPEN_MENU=1 open -n "$install_dir/ScribaBar.app"

macos-use: macos-install macos-open-installed

macos-release:
    apps/macos/Scripts/package_zip.sh release

reset-cache:
    go run ./cmd/scriba cache reset

telegram-alerts:
    go run ./cmd/scriba telegram alerts
