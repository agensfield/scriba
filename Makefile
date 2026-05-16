SHELL := /bin/bash

.PHONY: build check macos-launch macos-package macos-release macos-start macos-test test

build:
	go build -o .build/scriba ./cmd/scriba

test:
	go test ./...

check:
	go test ./...
	go vet ./...
	staticcheck ./...
	golangci-lint run ./...
	gosec ./...
	govulncheck ./...
	swift test --package-path apps/macos

macos-test:
	swift test --package-path apps/macos

macos-start:
	apps/macos/Scripts/compile_and_run.sh --test --open-menu

macos-launch:
	SCRIBABAR_OPEN_MENU=1 apps/macos/Scripts/launch.sh

macos-package:
	apps/macos/Scripts/package_app.sh debug

macos-release:
	apps/macos/Scripts/package_zip.sh release
