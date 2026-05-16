# ScribaBar

Native macOS menu bar shell for Scriba.

The app is intentionally thin: it resolves a usable `scriba` CLI once at startup,
loads `scriba status --fast --json` for the first paint, then refreshes in the
background. Usage logic stays in the native Go CLI/core under `cmd/scriba` and
`internal`.

## Development

```sh
swift build --package-path apps/macos
swift test --package-path apps/macos
apps/macos/Scripts/package_app.sh debug
apps/macos/Scripts/package_zip.sh release
apps/macos/Scripts/compile_and_run.sh --test --open-menu
apps/macos/Scripts/compile_and_run.sh --release-universal
SCRIBABAR_OPEN_MENU=1 apps/macos/Scripts/launch.sh
open apps/macos/.build/package/ScribaBar.app
SCRIBABAR_OPEN_MENU=1 open -n apps/macos/.build/package/ScribaBar.app
```

`ScribaBar` prefers a validated system `scriba` when its version is greater than
or equal to the bundled helper. Otherwise it falls back to the bundled native Go
helper at `ScribaBar.app/Contents/Helpers/scriba`.

The package script builds the Go `scriba` CLI before staging the app, writes
bundle metadata from the build version/git state, strips extended attributes,
signs helper/native binaries before the app, and smokes the bundled helper with
a stripped `PATH`. Debug builds use `com.agensfield.scribabar.debug`; release
builds use `com.agensfield.scribabar`. Set `ARCHES="arm64 x86_64"` or use
`compile_and_run.sh --release-universal` to build a universal app/helper. No
Bun, Node, JS resources, or shim are bundled.

`package_zip.sh` creates
`apps/macos/.build/artifacts/ScribaBar-macos-<arch>-<version>.zip` plus a
`.sha256` file, plus a dSYM zip when symbols are available. It validates the
staged app, zips with `ditto --norsrc`, extracts the archive to a temp
directory, verifies the extracted signature, and smokes the extracted bundled
helper with a stripped `PATH`.

The menu UI is native AppKit/SwiftUI with controlled custom rows, while the
settings window uses macOS 26 glass button styles when available. Settings
expose used/remaining meter mode, menu bar text mode, and refresh cadence. A
startup visibility check recreates the status item if macOS materializes it
incorrectly and points to Menu Bar settings if the system is hiding the icon.

Settings also exposes Telegram alert config backed by the shared Scriba CLI
config file at `~/.config/scriba/config.json`: enable/disable, bot token or bot
token env var, chat id, session/weekly thresholds, error inclusion, alert
preview, and send-now.

The app keeps a persisted baseline for weekly limit windows and refreshes in the
background every 10 minutes. If a weekly limit drops sharply before the
previously observed reset time, or its next reset window jumps forward while the
old reset was still in the future, ScribaBar sends a macOS notification. This is
intended to catch manual early resets of provider limits.

Notifications must be allowed in macOS Settings. The Settings window reports the
current notification authorization state so the watcher does not fail silently.

The menu is an `NSMenu` with native AppKit rows and a vibrant SwiftUI-hosted
summary item. Live menu screenshots should come from CleanShot or the user
desktop; SwiftUI preview artifacts are useful for layout regressions but do not
faithfully capture macOS menu-window vibrancy.
