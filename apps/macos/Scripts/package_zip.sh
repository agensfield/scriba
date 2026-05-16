#!/usr/bin/env bash
set -euo pipefail

CONF=${1:-release}
ROOT=$(cd "$(dirname "$0")/../../.." && pwd)
APP_ROOT="$ROOT/apps/macos"
APP="$APP_ROOT/.build/package/ScribaBar.app"
DSYM="$APP.dSYM"
ARTIFACT_DIR="$APP_ROOT/.build/artifacts"
TMP_DIR=$(mktemp -d "${TMPDIR:-/tmp}/scribabar-dist.XXXXXX")
DITTO_BIN=${DITTO_BIN:-/usr/bin/ditto}
source "$APP_ROOT/Scripts/release_artifacts.sh"

cleanup() {
  rm -rf "$TMP_DIR"
}
trap cleanup EXIT INT TERM

"$APP_ROOT/Scripts/package_app.sh" "$CONF"

VERSION=$(/usr/bin/plutil -extract CFBundleShortVersionString raw -o - "$APP/Contents/Info.plist")
ARCHES=$(lipo -archs "$APP/Contents/MacOS/ScribaBar" | tr ' ' '+')
ARCH_LABEL_INPUT=$(printf '%s' "$ARCHES" | tr '+' ' ')
ZIP_NAME=$(scribabar_app_zip_name "$VERSION" "$ARCH_LABEL_INPUT")
ZIP_PATH="$ARTIFACT_DIR/$ZIP_NAME"
SHA_PATH="$ZIP_PATH.sha256"
DSYM_ZIP_NAME=$(scribabar_dsym_zip_name "$VERSION" "$ARCH_LABEL_INPUT")
DSYM_ZIP_PATH="$ARTIFACT_DIR/$DSYM_ZIP_NAME"
DSYM_SHA_PATH="$DSYM_ZIP_PATH.sha256"

mkdir -p "$ARTIFACT_DIR"
rm -f "$ZIP_PATH" "$SHA_PATH" "$DSYM_ZIP_PATH" "$DSYM_SHA_PATH"

if find "$APP" -name '._*' -print | grep -q .; then
  echo "ERROR: AppleDouble files found in app bundle before zipping." >&2
  exit 1
fi

codesign --verify --deep --strict --verbose=2 "$APP"
"$DITTO_BIN" --norsrc -c -k --keepParent "$APP" "$ZIP_PATH"
shasum -a 256 "$ZIP_PATH" > "$SHA_PATH"

"$DITTO_BIN" -x -k "$ZIP_PATH" "$TMP_DIR"
EXTRACTED_APP="$TMP_DIR/ScribaBar.app"
codesign --verify --deep --strict --verbose=2 "$EXTRACTED_APP"
env -i HOME="${HOME:-}" PATH="/usr/bin:/bin:/usr/sbin:/sbin" \
  "$EXTRACTED_APP/Contents/Helpers/scriba" --version >/dev/null
env -i HOME="${HOME:-}" PATH="/usr/bin:/bin:/usr/sbin:/sbin" \
  "$EXTRACTED_APP/Contents/Helpers/scriba" status --fast --json >/dev/null

if [[ -d "$DSYM" ]]; then
  "$DITTO_BIN" --norsrc -c -k --keepParent "$DSYM" "$DSYM_ZIP_PATH"
  shasum -a 256 "$DSYM_ZIP_PATH" > "$DSYM_SHA_PATH"
fi

echo "Created $ZIP_PATH"
echo "Checksum $SHA_PATH"
if [[ -f "$DSYM_ZIP_PATH" ]]; then
  echo "Created $DSYM_ZIP_PATH"
  echo "Checksum $DSYM_SHA_PATH"
fi
