import AppKit
import SwiftUI

struct ScribaSettingsView: View {
    @ObservedObject var model: ScribaBarModel

    var body: some View {
        VStack(spacing: 0) {
            header
            ScrollView {
                VStack(alignment: .leading, spacing: 16) {
                    displaySection
                    systemSection
                    telegramSection
                }
                .padding(.horizontal, 22)
                .padding(.vertical, 18)
            }
            footer
        }
        .background {
            ZStack {
                Color(nsColor: .windowBackgroundColor)
                LinearGradient(
                    colors: [MenuPalette.codex.opacity(0.12), .clear, MenuPalette.claude.opacity(0.08)],
                    startPoint: .topLeading,
                    endPoint: .bottomTrailing)
            }
        }
        .frame(minWidth: 620, minHeight: 660)
    }

    private var header: some View {
        HStack(spacing: 14) {
            ZStack {
                RoundedRectangle(cornerRadius: 14, style: .continuous)
                    .fill(.regularMaterial)
                    .overlay {
                        RoundedRectangle(cornerRadius: 14, style: .continuous)
                            .stroke(.white.opacity(0.12), lineWidth: 1)
                    }
                Image(nsImage: StatusItemIconRenderer.makeIcon(
                    primaryPercent: model.snapshot?.overview.codexFiveHour?.displayPercent(mode: model.usagePercentMode),
                    secondaryPercent: model.snapshot?.overview.codexWeekly?.displayPercent(mode: model.usagePercentMode),
                    isStale: model.snapshot == nil))
                    .resizable()
                    .interpolation(.none)
                    .frame(width: 30, height: 24)
            }
            .frame(width: 48, height: 48)

            VStack(alignment: .leading, spacing: 3) {
                Text("ScribaBar")
                    .font(.title2.weight(.semibold))
                Text("Native macOS shell over Scriba usage telemetry.")
                    .foregroundStyle(.secondary)
                    .font(.callout)
            }
            Spacer()
        }
        .padding(.horizontal, 22)
        .padding(.top, 20)
        .padding(.bottom, 18)
        .background(.thinMaterial)
    }

    private var displaySection: some View {
        SettingsSection(title: "Display", systemImage: "menubar.rectangle") {
            SettingsRow(title: "Meter") {
                Picker("Meter", selection: $model.usagePercentMode) {
                    ForEach(UsagePercentMode.allCases) { mode in
                        Text(mode.label).tag(mode)
                    }
                }
                .labelsHidden()
                .pickerStyle(.segmented)
                .frame(maxWidth: 280)
            }

            SettingsRow(title: "Menu bar") {
                Picker("Menu bar text", selection: $model.menuBarTextMode) {
                    ForEach(MenuBarTextMode.allCases) { mode in
                        Text(mode.label).tag(mode)
                    }
                }
                .labelsHidden()
                .pickerStyle(.segmented)
                .frame(maxWidth: 420)
            }

            SettingsRow(title: "Refresh") {
                Picker("Refresh", selection: $model.refreshCadence) {
                    ForEach(RefreshCadence.allCases) { cadence in
                        Text(cadence.label).tag(cadence)
                    }
                }
                .labelsHidden()
                .pickerStyle(.segmented)
                .frame(maxWidth: 420)
            }
        }
    }

    private var systemSection: some View {
        SettingsSection(title: "System", systemImage: "terminal") {
            SettingsGridRow(label: "CLI", value: model.cliDescription)
            SettingsGridRow(label: "Status", value: statusText)
            SettingsGridRow(label: "Notifications", value: model.notificationDescription)
            SettingsGridRow(label: "Config", value: configPath.path)
            SettingsGridRow(label: "Cache", value: cachePath.path)
        }
    }

    private var telegramSection: some View {
        SettingsSection(title: "Telegram", systemImage: "paperplane") {
            if let result = model.telegramResult {
                TelegramStatusBanner(result: result)
            }

            SettingsRow(title: "Delivery") {
                Toggle("Enabled", isOn: $model.telegramSettings.enabled)
                    .toggleStyle(.switch)
            }

            SettingsRow(title: "Bot token") {
                SecureField(model.telegramSettings.hasBotToken ? "Stored in config" : "Paste bot token", text: $model.telegramBotTokenInput)
                    .textFieldStyle(.roundedBorder)
            }

            SettingsRow(title: "Token env") {
                TextField("SCRIBA_TELEGRAM_BOT_TOKEN", text: $model.telegramSettings.botTokenEnv)
                    .textFieldStyle(.roundedBorder)
            }

            SettingsRow(title: "Chat ID") {
                TextField("123456789", text: $model.telegramSettings.chatId)
                    .textFieldStyle(.roundedBorder)
            }

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
            .padding(.top, 2)

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
                Button {
                    Task { await model.runTelegramAlerts(send: false) }
                } label: {
                    Label("Preview", systemImage: "eye")
                }
                .disabled(model.isRunningTelegram)
                .scribaButtonStyle()
                Button {
                    Task { await model.runTelegramAlerts(send: true) }
                } label: {
                    Label("Send", systemImage: "paperplane.fill")
                }
                .disabled(model.isRunningTelegram)
                .scribaButtonStyle()
            }
        }
    }

    private var footer: some View {
        HStack(spacing: 10) {
            Button {
                openFolder(configPath.deletingLastPathComponent())
            } label: {
                Label("Config", systemImage: "folder")
            }
            .scribaButtonStyle()

            Button {
                openFolder(cachePath)
            } label: {
                Label("Cache", systemImage: "externaldrive")
            }
            .scribaButtonStyle()

            Spacer()

            Button {
                Task { await model.refresh() }
            } label: {
                Label("Refresh Now", systemImage: "arrow.clockwise")
            }
            .keyboardShortcut("r", modifiers: .command)
            .scribaButtonStyle(prominent: true)
        }
        .padding(.horizontal, 22)
        .padding(.vertical, 16)
        .background(.thinMaterial)
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
        SettingsRow(title: label) {
            Text(value)
                .lineLimit(1)
                .truncationMode(.middle)
                .textSelection(.enabled)
        }
    }
}

private struct SettingsSection<Content: View>: View {
    let title: String
    let systemImage: String
    @ViewBuilder var content: Content

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Label(title, systemImage: systemImage)
                .font(.headline.weight(.semibold))
                .foregroundStyle(MenuPalette.primary)
            VStack(alignment: .leading, spacing: 12) {
                content
            }
        }
        .padding(16)
        .background(.regularMaterial, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
        .overlay {
            RoundedRectangle(cornerRadius: 18, style: .continuous)
                .stroke(.white.opacity(0.10), lineWidth: 1)
        }
    }
}

private struct SettingsRow<Content: View>: View {
    let title: String
    @ViewBuilder var content: Content

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: 18) {
            Text(title)
                .foregroundStyle(.secondary)
                .frame(width: 92, alignment: .leading)
            content
                .frame(maxWidth: .infinity, alignment: .leading)
        }
        .font(.callout)
    }
}

private struct TelegramStatusBanner: View {
    let result: DoctorResult

    var body: some View {
        HStack(alignment: .top, spacing: 10) {
            Image(systemName: result.title.localizedCaseInsensitiveContains("failed") ? "exclamationmark.triangle.fill" : "checkmark.circle.fill")
                .foregroundStyle(result.title.localizedCaseInsensitiveContains("failed") ? .orange : .green)
            VStack(alignment: .leading, spacing: 3) {
                Text(result.title)
                    .font(.callout.weight(.semibold))
                Text(result.message.isEmpty ? "No output." : result.message)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .lineLimit(3)
                    .textSelection(.enabled)
            }
            Spacer()
        }
        .padding(12)
        .background(.thinMaterial, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
    }
}
