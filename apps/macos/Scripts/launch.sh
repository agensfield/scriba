#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/../../.." && pwd)
APP_ROOT="$ROOT/apps/macos"
APP_BUNDLE="$APP_ROOT/.build/package/ScribaBar.app"

if [[ ! -d "$APP_BUNDLE" ]]; then
  echo "ERROR: $APP_BUNDLE does not exist. Run apps/macos/Scripts/package_app.sh debug first." >&2
  exit 1
fi

if [[ "${SCRIBABAR_OPEN_MENU:-0}" == "1" ]]; then
  SCRIBABAR_OPEN_MENU=1 open -n "$APP_BUNDLE"
else
  open -n "$APP_BUNDLE"
fi
