#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/../../.." && pwd)
APP_ROOT="$ROOT/apps/macos"
APP_BUNDLE="$APP_ROOT/.build/package/ScribaBar.app"
APP_PROCESS_PATTERN="ScribaBar.app/Contents/MacOS/ScribaBar"
DEBUG_PROCESS_PATTERN="$APP_ROOT/.build/debug/ScribaBar"
RELEASE_PROCESS_PATTERN="$APP_ROOT/.build/release/ScribaBar"
LOCK_KEY="$(printf '%s' "$ROOT" | shasum -a 256 | cut -c1-8)"
LOCK_DIR="${TMPDIR:-/tmp}/scribabar-compile-and-run-${LOCK_KEY}"
LOCK_PID_FILE="$LOCK_DIR/pid"
WAIT_FOR_LOCK=0
RUN_TESTS=0
OPEN_MENU=0
CONF=debug
RELEASE_ARCHES=""

log() { printf '%s\n' "$*"; }
fail() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }

cleanup() {
  rm -rf "$LOCK_DIR"
}
trap cleanup EXIT INT TERM

acquire_lock() {
  while true; do
    if mkdir "$LOCK_DIR" 2>/dev/null; then
      echo "$$" > "$LOCK_PID_FILE"
      return
    fi

    local existing_pid=""
    if [[ -f "$LOCK_PID_FILE" ]]; then
      existing_pid="$(cat "$LOCK_PID_FILE" 2>/dev/null || true)"
    fi

    if [[ -n "$existing_pid" ]] && kill -0 "$existing_pid" 2>/dev/null; then
      if [[ "$WAIT_FOR_LOCK" == "1" ]]; then
        log "==> another ScribaBar build is running (pid $existing_pid); waiting"
        while kill -0 "$existing_pid" 2>/dev/null; do
          sleep 1
        done
        continue
      fi
      log "==> another ScribaBar build is running (pid $existing_pid); re-run with --wait"
      exit 0
    fi

    rm -rf "$LOCK_DIR"
  done
}

is_running() {
  pgrep -f "$APP_PROCESS_PATTERN" >/dev/null 2>&1 ||
    pgrep -f "$DEBUG_PROCESS_PATTERN" >/dev/null 2>&1 ||
    pgrep -f "$RELEASE_PROCESS_PATTERN" >/dev/null 2>&1 ||
    pgrep -x ScribaBar >/dev/null 2>&1
}

kill_all_scribabar() {
  osascript -e 'tell application "ScribaBar" to quit' >/dev/null 2>&1 || true
  for _ in {1..25}; do
    pkill -f "$APP_PROCESS_PATTERN" 2>/dev/null || true
    pkill -f "$DEBUG_PROCESS_PATTERN" 2>/dev/null || true
    pkill -f "$RELEASE_PROCESS_PATTERN" 2>/dev/null || true
    pkill -x ScribaBar 2>/dev/null || true
    if ! is_running; then
      return
    fi
    sleep 0.2
  done

  pkill -9 -f "$APP_PROCESS_PATTERN" 2>/dev/null || true
  pkill -9 -f "$DEBUG_PROCESS_PATTERN" 2>/dev/null || true
  pkill -9 -f "$RELEASE_PROCESS_PATTERN" 2>/dev/null || true
  pkill -9 -x ScribaBar 2>/dev/null || true

  for _ in {1..25}; do
    if ! is_running; then
      return
    fi
    sleep 0.2
  done
  fail "failed to stop existing ScribaBar instances"
}

run_step() {
  local label="$1"
  shift
  log "==> $label"
  "$@" || fail "$label failed"
}

for arg in "$@"; do
  case "$arg" in
    --wait|-w) WAIT_FOR_LOCK=1 ;;
    --test|-t) RUN_TESTS=1 ;;
    --open-menu|-m) OPEN_MENU=1 ;;
    --release) CONF=release ;;
    --debug) CONF=debug ;;
    --release-universal) CONF=release; RELEASE_ARCHES="arm64 x86_64" ;;
    --release-arches=*) CONF=release; RELEASE_ARCHES="${arg#*=}" ;;
    --help|-h)
      log "Usage: $(basename "$0") [--wait] [--test] [--open-menu] [--debug|--release] [--release-universal] [--release-arches=\"arm64 x86_64\"]"
      exit 0
      ;;
    *) fail "unknown argument: $arg" ;;
  esac
done

acquire_lock
log "==> killing existing ScribaBar instances"
kill_all_scribabar

if [[ "$RUN_TESTS" == "1" ]]; then
  run_step "swift test" swift test --package-path "$APP_ROOT"
fi
if [[ -n "$RELEASE_ARCHES" ]]; then
  run_step "package app" env ARCHES="$RELEASE_ARCHES" "$APP_ROOT/Scripts/package_app.sh" "$CONF"
else
  run_step "package app" "$APP_ROOT/Scripts/package_app.sh" "$CONF"
fi

log "==> launch app"
if [[ "$OPEN_MENU" == "1" ]]; then
  SCRIBABAR_OPEN_MENU=1 open -n "$APP_BUNDLE"
else
  open -n "$APP_BUNDLE"
fi

for _ in {1..12}; do
  if pgrep -f "$APP_PROCESS_PATTERN" >/dev/null 2>&1; then
    log "OK: ScribaBar is running."
    exit 0
  fi
  sleep 0.4
done

fail "ScribaBar exited immediately. Check Console.app user crash reports."
