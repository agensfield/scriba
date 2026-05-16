#!/usr/bin/env bash
set -euo pipefail

CONF=${1:-release}
ROOT=$(cd "$(dirname "$0")/../../.." && pwd)
APP_ROOT="$ROOT/apps/macos"
APP_STAGE="$APP_ROOT/.build/package/ScribaBar.app"
APP="$APP_STAGE"
SIGNING_MODE=${SCRIBABAR_SIGNING:-}
LOWER_CONF=$(printf "%s" "$CONF" | tr '[:upper:]' '[:lower:]')

ARCH_LIST=( ${ARCHES:-} )
if [[ ${#ARCH_LIST[@]} -eq 0 ]]; then
  case "$(uname -m)" in
    arm64) ARCH_LIST=(arm64) ;;
    x86_64) ARCH_LIST=(x86_64) ;;
    *) ARCH_LIST=("$(uname -m)") ;;
  esac
fi

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

MARKETING_VERSION=$(awk '/Version = / { gsub(/"/, "", $3); print $3; exit }' "$ROOT/internal/buildinfo/buildinfo.go")
BUILD_NUMBER=$(git -C "$ROOT" rev-list --count HEAD 2>/dev/null || echo "1")
GIT_COMMIT=$(git -C "$ROOT" rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_TIMESTAMP=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
BUNDLE_ID="com.agensfield.scribabar"
if [[ "$LOWER_CONF" == "debug" ]]; then
  BUNDLE_ID="com.agensfield.scribabar.debug"
fi

for ARCH in "${ARCH_LIST[@]}"; do
  swift build --package-path "$APP_ROOT" -c "$CONF" --arch "$ARCH" --product ScribaBar
done

rm -rf "$APP_STAGE"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources" "$APP/Contents/Helpers"

swift_binary_path() {
  local arch="$1"
  local candidate="$APP_ROOT/.build/${arch}-apple-macosx/$CONF/ScribaBar"
  if [[ -f "$candidate" ]]; then
    printf '%s\n' "$candidate"
    return
  fi
  candidate="$APP_ROOT/.build/$CONF/ScribaBar"
  if [[ -f "$candidate" ]]; then
    printf '%s\n' "$candidate"
    return
  fi
  return 1
}

install_universal_binary() {
  local name="$1"
  local dest="$2"
  shift 2
  local binaries=("$@")
  if [[ ${#binaries[@]} -gt 1 ]]; then
    lipo -create "${binaries[@]}" -output "$dest"
  else
    cp "${binaries[0]}" "$dest"
  fi
  chmod +x "$dest"
}

swift_bins=()
for ARCH in "${ARCH_LIST[@]}"; do
  swift_bins+=("$(swift_binary_path "$ARCH")")
done
install_universal_binary "ScribaBar" "$APP/Contents/MacOS/ScribaBar" "${swift_bins[@]}"
chmod +x "$APP/Contents/MacOS/ScribaBar"

go_bins=()
for ARCH in "${ARCH_LIST[@]}"; do
  out="$APP_ROOT/.build/package/scriba-$ARCH"
  GOARCH_VALUE="$ARCH"
  if [[ "$ARCH" == "x86_64" ]]; then
    GOARCH_VALUE="amd64"
  fi
  GOOS=darwin GOARCH="$GOARCH_VALUE" CGO_ENABLED=0 go build \
    -ldflags "-X github.com/agensfield/scriba/internal/buildinfo.Version=${MARKETING_VERSION} -X github.com/agensfield/scriba/internal/buildinfo.Commit=${GIT_COMMIT}" \
    -o "$out" \
    "$ROOT/cmd/scriba"
  go_bins+=("$out")
done
install_universal_binary "scriba" "$APP/Contents/Helpers/scriba" "${go_bins[@]}"
chmod +x "$APP/Contents/Helpers/scriba"
env -i HOME="${HOME:-}" PATH="/usr/bin:/bin:/usr/sbin:/sbin" "$APP/Contents/Helpers/scriba" --version >/dev/null

"$APP_ROOT/Scripts/build_icon.sh" "$APP/Contents/Resources/AppIcon.icns"

cat > "$APP/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleName</key><string>ScribaBar</string>
    <key>CFBundleDisplayName</key><string>ScribaBar</string>
    <key>CFBundleIdentifier</key><string>${BUNDLE_ID}</string>
    <key>CFBundleExecutable</key><string>ScribaBar</string>
    <key>CFBundleIconFile</key><string>AppIcon</string>
    <key>CFBundlePackageType</key><string>APPL</string>
    <key>CFBundleShortVersionString</key><string>${MARKETING_VERSION}</string>
    <key>CFBundleVersion</key><string>${BUILD_NUMBER}</string>
    <key>LSMinimumSystemVersion</key><string>14.0</string>
    <key>LSUIElement</key><true/>
    <key>ScribaGitCommit</key><string>${GIT_COMMIT}</string>
    <key>ScribaBuildTimestamp</key><string>${BUILD_TIMESTAMP}</string>
</dict>
</plist>
PLIST

if command -v dsymutil >/dev/null 2>&1; then
  rm -rf "$APP_STAGE.dSYM"
  dsymutil "$APP/Contents/MacOS/ScribaBar" -o "$APP_STAGE.dSYM" >/dev/null 2>&1 || true
fi

chmod -R u+w "$APP"
xattr -cr "$APP"
find "$APP" -name '._*' -delete
while IFS= read -r -d '' helper; do
  codesign "${CODESIGN_ARGS[@]}" "$helper"
done < <(find "$APP/Contents/Helpers" -type f \( -perm -111 -o -name '*.node' \) -print0)
codesign "${CODESIGN_ARGS[@]}" "$APP/Contents/MacOS/ScribaBar"
codesign "${CODESIGN_ARGS[@]}" "$APP"

echo "Created $APP"
