# ScribaBar

Native macOS menu bar shell for Scriba.

The app is intentionally thin: it resolves a usable `scriba` CLI once at startup,
loads `scriba status --fast --json` for the first paint, then refreshes in the
background. Usage logic stays in `packages/scriba`.

## Development

```sh
swift build --package-path apps/macos
swift test --package-path apps/macos
bun run macos:preview
apps/macos/Scripts/package_app.sh debug
apps/macos/Scripts/package_zip.sh release
apps/macos/Scripts/compile_and_run.sh --test --open-menu
open apps/macos/.build/package/ScribaBar.app
SCRIBABAR_OPEN_MENU=1 open -n apps/macos/.build/package/ScribaBar.app
```

`ScribaBar` prefers a validated system `scriba` when its version is greater than
or equal to the bundled helper. Otherwise it falls back to the bundled helper at
`ScribaBar.app/Contents/Helpers/scriba`.

The package script builds the TypeScript CLI before staging the app, writes
bundle metadata from the package version/git state, strips extended attributes,
signs helper/native binaries before the app, and smokes the bundled helper with
a stripped `PATH`. The helper is a small native shim named `scriba`; it prefers
the bundled Bun runtime and still seeds common Homebrew/Bun/Node locations
because a LaunchServices-launched menu bar app does not inherit an interactive
shell environment.

`package_zip.sh` creates
`apps/macos/.build/artifacts/ScribaBar-macos-<arch>-<version>.zip` plus a
`.sha256` file. It validates the staged app, zips with `ditto --norsrc`,
extracts the archive to a temp directory, verifies the extracted signature, and
smokes the extracted bundled helper with a stripped `PATH`.

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
