import AppKit
import SwiftUI

struct ScribaSettingsView: View {
    @ObservedObject var model: ScribaBarModel

    var body: some View {
        VStack(alignment: .leading, spacing: 18) {
            HStack(spacing: 12) {
                Image(nsImage: StatusItemIconRenderer.makeIcon(
                    primaryUsed: model.snapshot?.overview.codexFiveHour?.usedPercent,
                    secondaryUsed: model.snapshot?.overview.codexWeekly?.usedPercent,
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

            Grid(alignment: .leadingFirstTextBaseline, horizontalSpacing: 18, verticalSpacing: 10) {
                SettingsGridRow(label: "CLI", value: model.cliDescription)
                SettingsGridRow(label: "Status", value: statusText)
                SettingsGridRow(label: "Notifications", value: model.notificationDescription)
                SettingsGridRow(label: "Config", value: configPath.path)
                SettingsGridRow(label: "Cache", value: cachePath.path)
            }

            Spacer()

            HStack {
                Button("Open Config Folder") {
                    openFolder(configPath.deletingLastPathComponent())
                }
                Button("Open Cache Folder") {
                    openFolder(cachePath)
                }
                Spacer()
                Button("Refresh Now") {
                    Task { await model.refresh() }
                }
                .keyboardShortcut("r", modifiers: .command)
            }
        }
        .padding(20)
        .frame(minWidth: 440, minHeight: 310)
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
