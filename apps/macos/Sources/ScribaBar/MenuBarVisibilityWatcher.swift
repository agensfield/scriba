import AppKit

struct StatusItemVisibilitySnapshot: Equatable, CustomStringConvertible {
    let isVisible: Bool
    let hasButton: Bool
    let hasWindow: Bool
    let hasScreen: Bool
    let buttonWidth: CGFloat

    var description: String {
        "visible=\(isVisible),button=\(hasButton),window=\(hasWindow),screen=\(hasScreen),width=\(Int(buttonWidth.rounded()))"
    }
}

enum MenuBarVisibilityWatcher {
    static let startupCheckDelay: TimeInterval = 2
    static let startupFreshnessInterval: TimeInterval = 10
    static let guidanceRepeatInterval: TimeInterval = 24 * 60 * 60
    static let settingsURL = URL(string: "x-apple.systempreferences:com.apple.MenuBarSettings")!

    @MainActor
    static func visibilitySnapshot(_ item: NSStatusItem) -> StatusItemVisibilitySnapshot {
        StatusItemVisibilitySnapshot(
            isVisible: item.isVisible,
            hasButton: item.button != nil,
            hasWindow: item.button?.window != nil,
            hasScreen: item.button?.window?.screen != nil,
            buttonWidth: item.button?.frame.size.width ?? 0)
    }

    static func isBlockedSnapshot(_ snapshot: StatusItemVisibilitySnapshot) -> Bool {
        guard snapshot.isVisible else { return false }
        guard snapshot.hasButton else { return true }
        return !snapshot.hasWindow || !snapshot.hasScreen || snapshot.buttonWidth <= 0
    }

    static func shouldAttemptStartupRecovery(
        appLaunchedAt: Date,
        now: Date = Date(),
        snapshot: StatusItemVisibilitySnapshot)
        -> Bool
    {
        guard now.timeIntervalSince(appLaunchedAt) <= startupFreshnessInterval else { return false }
        return isBlockedSnapshot(snapshot)
    }

    static func shouldShowGuidance(userDefaults: UserDefaults, now: Date = Date()) -> Bool {
        guard userDefaults.bool(forKey: ScribaBarDefaults.menuVisibilityGuidanceShown) else {
            return true
        }

        let lastShownAt = userDefaults.double(forKey: ScribaBarDefaults.menuVisibilityGuidanceLastShownAt)
        guard lastShownAt > 0 else { return false }
        return now.timeIntervalSince1970 - lastShownAt >= guidanceRepeatInterval
    }

    static func markGuidanceShown(userDefaults: UserDefaults, now: Date = Date()) {
        userDefaults.set(true, forKey: ScribaBarDefaults.menuVisibilityGuidanceShown)
        userDefaults.set(now.timeIntervalSince1970, forKey: ScribaBarDefaults.menuVisibilityGuidanceLastShownAt)
    }

    @MainActor
    static func presentGuidance(userDefaults: UserDefaults, now: Date = Date()) {
        markGuidanceShown(userDefaults: userDefaults, now: now)

        let alert = NSAlert()
        alert.messageText = "ScribaBar is running, but macOS may be hiding it"
        alert.informativeText = "Open Menu Bar settings and allow ScribaBar if the icon is not visible."
        alert.alertStyle = .warning
        alert.addButton(withTitle: "Open Menu Bar Settings")
        alert.addButton(withTitle: "Dismiss")

        if alert.runModal() == .alertFirstButtonReturn {
            NSWorkspace.shared.open(settingsURL)
        }
    }
}
