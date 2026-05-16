import AppKit
import SwiftUI

struct ScribaSettingsView: View {
    @ObservedObject var model: ScribaBarModel

    var body: some View {
        VStack(alignment: .leading, spacing: 18) {
            HStack(spacing: 12) {
                Image(nsImage: StatusItemIconRenderer.makeIcon(
                    primaryPercent: model.snapshot?.overview.codexFiveHour?.displayPercent(mode: model.usagePercentMode),
                    secondaryPercent: model.snapshot?.overview.codexWeekly?.displayPercent(mode: model.usagePercentMode),
                    isStale: model.snapshot == nil))
                    .resizable()
                    .interpolation(.none)
                    .frame(width: 28, height: 22)

                VStack(alignment: .leading, spacing: 2) {
                    Text("ScribaBar")
                        .font(.title3.weight(.semibold))
                    Text("Native macOS shell over the Scriba CLI.")
                        .foregroundStyle(.secondary)
                        .font(.callout)
                }
            }

            Divider()

            VStack(alignment: .leading, spacing: 12) {
                Text("Display")
                    .font(.headline)
                Picker("Meter", selection: $model.usagePercentMode) {
                    ForEach(UsagePercentMode.allCases) { mode in
                        Text(mode.label).tag(mode)
                    }
                }
                .pickerStyle(.segmented)

                Picker("Menu bar text", selection: $model.menuBarTextMode) {
                    ForEach(MenuBarTextMode.allCases) { mode in
                        Text(mode.label).tag(mode)
                    }
                }
                .pickerStyle(.segmented)

                Picker("Refresh", selection: $model.refreshCadence) {
                    ForEach(RefreshCadence.allCases) { cadence in
                        Text(cadence.label).tag(cadence)
                    }
                }
                .pickerStyle(.segmented)
            }

            Divider()

            Grid(alignment: .leadingFirstTextBaseline, horizontalSpacing: 18, verticalSpacing: 10) {
                SettingsGridRow(label: "CLI", value: model.cliDescription)
                SettingsGridRow(label: "Status", value: statusText)
                SettingsGridRow(label: "Notifications", value: model.notificationDescription)
                SettingsGridRow(label: "Config", value: configPath.path)
                SettingsGridRow(label: "Cache", value: cachePath.path)
            }

            Divider()

            telegramSection

            Spacer()

            HStack {
                Button("Open Config Folder") {
                    openFolder(configPath.deletingLastPathComponent())
                }
                .scribaButtonStyle()
                Button("Open Cache Folder") {
                    openFolder(cachePath)
                }
                .scribaButtonStyle()
                Spacer()
                Button("Refresh Now") {
                    Task { await model.refresh() }
                }
                .keyboardShortcut("r", modifiers: .command)
                .scribaButtonStyle(prominent: true)
            }
        }
        .padding(20)
        .frame(minWidth: 560, minHeight: 620)
        .alert(item: $model.telegramResult) { result in
            Alert(
                title: Text(result.title),
                message: Text(result.message.isEmpty ? "No output." : result.message),
                dismissButton: .default(Text("OK")))
        }
    }

    private var telegramSection: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack(alignment: .firstTextBaseline) {
                Text("Telegram")
                    .font(.headline)
                Spacer()
                Toggle("Enabled", isOn: $model.telegramSettings.enabled)
                    .toggleStyle(.switch)
            }

            Grid(alignment: .leadingFirstTextBaseline, horizontalSpacing: 12, verticalSpacing: 8) {
                GridRow {
                    Text("Bot token")
                        .foregroundStyle(.secondary)
                    SecureField(model.telegramSettings.hasBotToken ? "Stored in config" : "Paste bot token", text: $model.telegramBotTokenInput)
                        .textFieldStyle(.roundedBorder)
                }
                GridRow {
                    Text("Token env")
                        .foregroundStyle(.secondary)
                    TextField("SCRIBA_TELEGRAM_BOT_TOKEN", text: $model.telegramSettings.botTokenEnv)
                        .textFieldStyle(.roundedBorder)
                }
                GridRow {
                    Text("Chat ID")
                        .foregroundStyle(.secondary)
                    TextField("123456789", text: $model.telegramSettings.chatId)
                        .textFieldStyle(.roundedBorder)
                }
            }
            .font(.callout)

            HStack(spacing: 12) {
                LabeledContent("Session") {
                    Stepper("\(Int(model.telegramSettings.sessionPercent.rounded()))%", value: $model.telegramSettings.sessionPercent, in: 1...100, step: 5)
                }
                LabeledContent("Weekly") {
                    Stepper("\(Int(model.telegramSettings.weeklyPercent.rounded()))%", value: $model.telegramSettings.weeklyPercent, in: 1...100, step: 5)
                }
                Toggle("Errors", isOn: $model.telegramSettings.includeErrors)
            }
            .font(.callout)

            HStack {
                Text(model.telegramSettings.path.isEmpty ? configPath.path : model.telegramSettings.path)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                    .truncationMode(.middle)
                Spacer()
                Button(model.isSavingTelegram ? "Saving..." : "Save") {
                    Task { await model.saveTelegramSettings() }
                }
                .disabled(model.isSavingTelegram)
                .scribaButtonStyle(prominent: true)
                Button("Preview Alerts") {
                    Task { await model.runTelegramAlerts(send: false) }
                }
                .disabled(model.isRunningTelegram)
                .scribaButtonStyle()
                Button("Send Now") {
                    Task { await model.runTelegramAlerts(send: true) }
                }
                .disabled(model.isRunningTelegram)
                .scribaButtonStyle()
            }
        }
    }

    private var statusText: String {
        switch model.state {
        case .loading:
            "Loading"
        case .ready:
            model.snapshot?.generatedDate.map {
                "Updated \($0.formatted(.relative(presentation: .named)))"
            } ?? "Ready"
        case let .failed(message):
            "Failed: \(message)"
        }
    }

    private var configPath: URL {
        FileManager.default.homeDirectoryForCurrentUser
            .appendingPathComponent(".config/scriba/config.json")
    }

    private var cachePath: URL {
        FileManager.default.homeDirectoryForCurrentUser
            .appendingPathComponent(".cache/scriba", isDirectory: true)
    }

    private func openFolder(_ url: URL) {
        try? FileManager.default.createDirectory(at: url, withIntermediateDirectories: true)
        NSWorkspace.shared.open(url)
    }
}

private struct SettingsGridRow: View {
    let label: String
    let value: String

    var body: some View {
        GridRow {
            Text(label)
                .foregroundStyle(.secondary)
            Text(value)
                .lineLimit(1)
                .truncationMode(.middle)
                .textSelection(.enabled)
        }
        .font(.callout)
    }
}
