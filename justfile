set shell := ["bash", "-eu", "-o", "pipefail", "-c"]

alias b := build
alias t := test
alias g := check
alias m := macos-start
alias mp := macos-package
alias ml := macos-launch
alias mr := macos-release

default:
    @just --list

build:
    go build -o .build/scriba ./cmd/scriba

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

check: test lint macos-test

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

macos-release:
    apps/macos/Scripts/package_zip.sh release

reset-cache:
    go run ./cmd/scriba cache reset

telegram-alerts:
    go run ./cmd/scriba telegram alerts
