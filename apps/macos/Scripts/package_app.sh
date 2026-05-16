#!/usr/bin/env bash
set -euo pipefail

CONF=${1:-release}
ROOT=$(cd "$(dirname "$0")/../../.." && pwd)
APP_ROOT="$ROOT/apps/macos"
APP_STAGE="$APP_ROOT/.build/package/ScribaBar.app"
APP="$APP_STAGE"
SIGNING_MODE=${SCRIBABAR_SIGNING:-}

has_signing_identity() {
  local identity="${1:-}"
  [[ -n "$identity" ]] || return 1
  security find-identity -p codesigning -v 2>/dev/null | grep -F "$identity" >/dev/null 2>&1
}

detect_codesigning_identity() {
  local identities
  identities="$(security find-identity -p codesigning -v 2>/dev/null || true)"
  awk '
    index($0, "\"Developer ID Application:") ||
    index($0, "\"Apple Development:") ||
    index($0, "\"Apple Distribution:") {
      sub(/^[^\"]*\"/, "")
      sub(/\".*$/, "")
      print
      exit
    }
  ' <<<"$identities"
}

resolve_codesign_args() {
  if [[ "$SIGNING_MODE" == "adhoc" ]]; then
    CODESIGN_ARGS=(--force --sign -)
    return
  fi

  if [[ -n "${APP_IDENTITY:-}" ]]; then
    if has_signing_identity "$APP_IDENTITY"; then
      CODESIGN_ARGS=(--force --timestamp --options runtime --sign "$APP_IDENTITY")
      return
    fi
    echo "WARN: APP_IDENTITY not found in Keychain; falling back to ad-hoc signing." >&2
    CODESIGN_ARGS=(--force --sign -)
    return
  fi

  local detected
  detected="$(detect_codesigning_identity)"
  if [[ -n "$detected" && "$SIGNING_MODE" != "adhoc" ]]; then
    CODESIGN_ARGS=(--force --timestamp --options runtime --sign "$detected")
    return
  fi

  CODESIGN_ARGS=(--force --sign -)
}

resolve_codesign_args

bun --filter @agensfield/scriba build
MARKETING_VERSION=$(bun -e "console.log(JSON.parse(await Bun.file('$ROOT/packages/scriba/package.json').text()).version)")
BUILD_NUMBER=$(git -C "$ROOT" rev-list --count HEAD 2>/dev/null || echo "1")
GIT_COMMIT=$(git -C "$ROOT" rev-parse --short HEAD 2>/dev/null || echo "unknown")

swift build --package-path "$APP_ROOT" -c "$CONF" --product ScribaBar
swift build --package-path "$APP_ROOT" -c "$CONF" --product ScribaCLIShim

BIN_DIR=$(swift build --package-path "$APP_ROOT" -c "$CONF" --product ScribaBar --show-bin-path)
rm -rf "$APP_STAGE"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources" "$APP/Contents/Helpers"

cp "$BIN_DIR/ScribaBar" "$APP/Contents/MacOS/ScribaBar"
chmod +x "$APP/Contents/MacOS/ScribaBar"

if [[ -f "$ROOT/packages/scriba/dist/cli.js" ]]; then
  cp "$BIN_DIR/ScribaCLIShim" "$APP/Contents/Helpers/scriba"
  chmod +x "$APP/Contents/Helpers/scriba"
  mkdir -p "$APP/Contents/Resources/ScribaCLI"
  cp "$ROOT/packages/scriba/dist/cli.js" "$APP/Contents/Resources/ScribaCLI/scriba-cli.js"
  LIBSQL_ENTRY=$(node -p "require.resolve('libsql', { paths: ['$ROOT/packages/scriba'] })")
  LIBSQL_NODE_MODULES=$(cd "$(dirname "$LIBSQL_ENTRY")/.." && pwd -P)
  mkdir -p "$APP/Contents/Resources/ScribaCLI/node_modules"
  cp -R -L "$LIBSQL_NODE_MODULES/." "$APP/Contents/Resources/ScribaCLI/node_modules/"
  BUN_BIN=$(command -v bun)
  cp -L "$BUN_BIN" "$APP/Contents/Helpers/bun"
  chmod +x "$APP/Contents/Helpers/bun"
  env -i HOME="${HOME:-}" PATH="/usr/bin:/bin:/usr/sbin:/sbin" "$APP/Contents/Helpers/scriba" --version >/dev/null
else
  echo "ERROR: Missing packages/scriba/dist/cli.js after build." >&2
  exit 1
fi

"$APP_ROOT/Scripts/build_icon.sh" "$APP/Contents/Resources/AppIcon.icns"

cat > "$APP/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleName</key><string>ScribaBar</string>
    <key>CFBundleDisplayName</key><string>ScribaBar</string>
    <key>CFBundleIdentifier</key><string>com.agensfield.scribabar</string>
    <key>CFBundleExecutable</key><string>ScribaBar</string>
    <key>CFBundleIconFile</key><string>AppIcon</string>
    <key>CFBundlePackageType</key><string>APPL</string>
    <key>CFBundleShortVersionString</key><string>${MARKETING_VERSION}</string>
    <key>CFBundleVersion</key><string>${BUILD_NUMBER}</string>
    <key>LSMinimumSystemVersion</key><string>14.0</string>
    <key>LSUIElement</key><true/>
    <key>ScribaGitCommit</key><string>${GIT_COMMIT}</string>
</dict>
</plist>
PLIST

chmod -R u+w "$APP"
xattr -cr "$APP"
find "$APP" -name '._*' -delete
while IFS= read -r -d '' helper; do
  codesign "${CODESIGN_ARGS[@]}" "$helper"
done < <(find "$APP/Contents/Helpers" -type f \( -perm -111 -o -name '*.node' \) -print0)
while IFS= read -r -d '' nativeModule; do
  codesign "${CODESIGN_ARGS[@]}" "$nativeModule"
done < <(find "$APP/Contents/Resources/ScribaCLI" -type f -name '*.node' -print0)
codesign "${CODESIGN_ARGS[@]}" "$APP/Contents/MacOS/ScribaBar"
codesign "${CODESIGN_ARGS[@]}" "$APP"

echo "Created $APP"
