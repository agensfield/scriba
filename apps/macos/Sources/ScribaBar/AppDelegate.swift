import AppKit
import SwiftUI
@preconcurrency import UserNotifications

@MainActor
final class AppDelegate: NSObject, NSApplicationDelegate, UNUserNotificationCenterDelegate {
    private static let menuWidth: CGFloat = 310
    private static let summaryHeight: CGFloat = 326
    private static let historyMenuWidth: CGFloat = 390
    private static let historyMenuHeight: CGFloat = 208

    private var statusItem: NSStatusItem?
    private var model: ScribaBarModel?
    private var settingsWindow: NSWindow?

    func applicationDidFinishLaunching(_ notification: Notification) {
        NSApp.setActivationPolicy(.accessory)
        guard Self.claimSingleInstance() else {
            NSApp.terminate(nil)
            return
        }

        let model = ScribaBarModel()
        self.model = model
        configureNotifications(model: model)

        let statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
        statusItem.button?.image = StatusItemIconRenderer.makeIcon(
            primaryUsed: nil,
            secondaryUsed: nil,
            isStale: true)
        statusItem.button?.imagePosition = .imageLeading
        statusItem.button?.title = ""
        statusItem.button?.font = NSFont.monospacedDigitSystemFont(ofSize: 12, weight: .semibold)
        statusItem.menu = makeStatusMenu(model: model)
        self.statusItem = statusItem

        model.onSnapshotChanged = { [weak self] snapshot in
            DispatchQueue.main.async {
                self?.updateStatusItem(snapshot: snapshot)
            }
        }
        model.onWeeklyLimitResetEarly = { [weak self] event in
            self?.notifyWeeklyLimitReset(event)
        }

        Task {
            await model.start()
        }

        let environment = ProcessInfo.processInfo.environment
        if environment["SCRIBABAR_OPEN_MENU"] == "1" || environment["SCRIBABAR_OPEN_POPOVER"] == "1" {
            DispatchQueue.main.asyncAfter(deadline: .now() + 0.8) { [weak self] in
                self?.statusItem?.button?.performClick(nil)
            }
        }
    }

    private static func claimSingleInstance() -> Bool {
        guard let bundleIdentifier = Bundle.main.bundleIdentifier else { return true }
        let currentPID = ProcessInfo.processInfo.processIdentifier
        return !NSRunningApplication
            .runningApplications(withBundleIdentifier: bundleIdentifier)
            .contains { application in
                application.processIdentifier != currentPID && !application.isTerminated
            }
    }

    nonisolated func userNotificationCenter(
        _ center: UNUserNotificationCenter,
        willPresent notification: UNNotification)
        async
        -> UNNotificationPresentationOptions
    {
        [.banner, .sound]
    }

    private func configureNotifications(model: ScribaBarModel) {
        let center = UNUserNotificationCenter.current()
        center.delegate = self
        center.getNotificationSettings { settings in
            let status = settings.authorizationStatus
            DispatchQueue.main.async {
                model.notificationDescription = Self.notificationDescription(for: status)
            }
            guard status == .notDetermined else { return }
            UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound]) { _, _ in
                UNUserNotificationCenter.current().getNotificationSettings { updated in
                    let updatedStatus = updated.authorizationStatus
                    DispatchQueue.main.async {
                        model.notificationDescription = Self.notificationDescription(for: updatedStatus)
                    }
                }
            }
        }
    }

    private static func notificationDescription(for status: UNAuthorizationStatus) -> String {
        switch status {
        case .authorized:
            return "Allowed"
        case .denied:
            return "Denied in macOS Settings"
        case .notDetermined:
            return "Not requested"
        case .provisional:
            return "Allowed quietly"
        case .ephemeral:
            return "Allowed for this session"
        @unknown default:
            return "Unknown"
        }
    }

    private func makeStatusMenu(model: ScribaBarModel) -> NSMenu {
        let menu = NSMenu()
        menu.autoenablesItems = false
        menu.minimumWidth = Self.menuWidth

        let summaryItem = NSMenuItem()
        let summaryView = VibrantHostingView(
            rootView: NativeMenuSurfaceView(model: model)
                .environment(\.colorScheme, .dark))
        summaryView.frame = NSRect(x: 0, y: 0, width: Self.menuWidth, height: Self.summaryHeight)
        summaryItem.view = summaryView
        menu.addItem(summaryItem)
        menu.addItem(dividerItem())

        menu.addItem(usageHistoryItem(model: model))
        menu.addItem(refreshItem(model: model))
        menu.addItem(actionItem(
            title: "Doctor",
            systemImage: "stethoscope",
            action: { [weak self] in self?.runDoctorFromMenu(nil) }))
        menu.addItem(actionItem(
            title: "Settings...",
            systemImage: "gearshape",
            shortcut: "⌘ ,",
            action: { [weak self] in self?.openSettingsFromMenu(nil) }))
        menu.addItem(dividerItem())
        menu.addItem(actionItem(
            title: "About ScribaBar",
            systemImage: "info.circle",
            action: { [weak self] in self?.showAboutFromMenu(nil) }))
        menu.addItem(actionItem(
            title: "Quit",
            systemImage: "power",
            shortcut: "⌘ Q",
            action: { [weak self] in self?.quitFromMenu(nil) }))

        return menu
    }

    private func usageHistoryItem(model: ScribaBarModel) -> NSMenuItem {
        let item = NSMenuItem(title: "Usage History", action: nil, keyEquivalent: "")
        let row = VibrantHostingView(
            rootView: MenuActionRow(
                title: "Usage History",
                systemImage: "chart.bar.xaxis",
                width: Self.menuWidth,
                showsSubmenu: true)
                .environment(\.colorScheme, .dark))
        row.frame = NSRect(x: 0, y: 0, width: Self.menuWidth, height: 29)
        item.view = row

        let submenu = NSMenu()
        submenu.autoenablesItems = false
        submenu.minimumWidth = Self.historyMenuWidth

        let chartItem = NSMenuItem()
        let chartView = VibrantHostingView(
            rootView: UsageHistoryMenuView(model: model, width: Self.historyMenuWidth)
                .environment(\.colorScheme, .dark))
        chartView.frame = NSRect(x: 0, y: 0, width: Self.historyMenuWidth, height: Self.historyMenuHeight)
        chartItem.view = chartView
        submenu.addItem(chartItem)
        item.submenu = submenu
        return item
    }

    private func refreshItem(model: ScribaBarModel) -> NSMenuItem {
        let item = NSMenuItem()
        let refreshView = VibrantHostingView(
            rootView: RefreshMenuRow(model: model, width: Self.menuWidth)
                .environment(\.colorScheme, .dark))
        refreshView.frame = NSRect(x: 0, y: 0, width: Self.menuWidth, height: 29)
        item.view = refreshView
        return item
    }

    private func actionItem(
        title: String,
        systemImage: String,
        shortcut: String? = nil,
        action: @escaping () -> Void)
        -> NSMenuItem
    {
        let item = NSMenuItem()
        let row = VibrantHostingView(
            rootView: MenuActionRow(
                title: title,
                systemImage: systemImage,
                shortcut: shortcut,
                width: Self.menuWidth,
                action: action)
                .environment(\.colorScheme, .dark))
        row.frame = NSRect(x: 0, y: 0, width: Self.menuWidth, height: 29)
        item.view = row
        return item
    }

    private func dividerItem() -> NSMenuItem {
        let item = NSMenuItem()
        let row = VibrantHostingView(
            rootView: MenuDividerRow(width: Self.menuWidth)
                .environment(\.colorScheme, .dark))
        row.frame = NSRect(x: 0, y: 0, width: Self.menuWidth, height: 9)
        item.view = row
        return item
    }

    @objc private func runDoctorFromMenu(_ sender: AnyObject?) {
        Task { [weak self] in
            guard let self, let model = self.model else { return }
            await model.runDoctor()
            if let result = model.doctorResult {
                self.showDoctorResult(result)
            }
        }
    }

    @objc private func openSettingsFromMenu(_ sender: AnyObject?) {
        openSettings()
    }

    @objc private func showAboutFromMenu(_ sender: AnyObject?) {
        NSApp.orderFrontStandardAboutPanel(options: [
            .applicationName: "ScribaBar",
            .applicationVersion: Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "0.0.0-alpha.0",
            .version: Bundle.main.object(forInfoDictionaryKey: "CFBundleVersion") as? String ?? "1",
            .credits: NSAttributedString(string: "Native menu bar shell for Scriba usage telemetry."),
        ])
        NSApplication.shared.activate(ignoringOtherApps: true)
    }

    @objc private func quitFromMenu(_ sender: AnyObject?) {
        NSApplication.shared.terminate(nil)
    }

    private func openSettings() {
        guard let model else { return }

        if let settingsWindow {
            settingsWindow.makeKeyAndOrderFront(nil)
            NSApplication.shared.activate(ignoringOtherApps: true)
            return
        }

        let window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 440, height: 310),
            styleMask: [.titled, .closable, .miniaturizable],
            backing: .buffered,
            defer: false)
        window.title = "ScribaBar Settings"
        window.isReleasedWhenClosed = false
        window.contentViewController = NSHostingController(rootView: ScribaSettingsView(model: model))
        window.center()
        settingsWindow = window
        window.makeKeyAndOrderFront(nil)
        NSApplication.shared.activate(ignoringOtherApps: true)
    }

    private func showDoctorResult(_ result: DoctorResult) {
        let alert = NSAlert()
        alert.messageText = result.title
        alert.informativeText = result.message.isEmpty ? "No output." : result.message
        alert.alertStyle = result.title.localizedCaseInsensitiveContains("Failed") ? .warning : .informational
        alert.addButton(withTitle: "OK")
        NSApplication.shared.activate(ignoringOtherApps: true)
        alert.runModal()
    }

    private func notifyWeeklyLimitReset(_ event: WeeklyLimitResetEvent) {
        let content = UNMutableNotificationContent()
        content.title = event.title
        content.body = event.message
        content.sound = .default
        content.threadIdentifier = "scribabar-weekly-reset"

        let request = UNNotificationRequest(
            identifier: "weekly-reset-\(event.providerID)-\(event.label)-\(UUID().uuidString)",
            content: content,
            trigger: nil)
        UNUserNotificationCenter.current().add(request)
    }

    private func updateStatusItem(snapshot: StatusSnapshot?) {
        guard let button = statusItem?.button else { return }
        guard let snapshot else {
            button.title = ""
            button.image = StatusItemIconRenderer.makeIcon(
                primaryUsed: nil,
                secondaryUsed: nil,
                isStale: true)
            return
        }

        let overview = snapshot.overview
        button.image = StatusItemIconRenderer.makeIcon(
            primaryUsed: overview.codexFiveHour?.usedPercent,
            secondaryUsed: overview.codexWeekly?.usedPercent,
            isStale: false)

        if let percent = overview.codexWeekly?.usedPercent {
            button.title = "S \(Int(percent.rounded()))%"
        } else {
            button.title = "S"
        }
    }
}

private final class VibrantHostingView<Content: View>: NSHostingView<Content> {
    override var allowsVibrancy: Bool {
        true
    }
}
