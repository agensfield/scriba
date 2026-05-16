import AppKit
import SwiftUI
import Testing
@testable import ScribaBar

@MainActor
@Suite("menu bar preview render")
struct MenuBarPreviewRenderTests {
    @Test("renders preview artifact when requested")
    func renderPreviewArtifact() throws {
        guard let outputPath = ProcessInfo.processInfo.environment["SCRIBABAR_PREVIEW_OUTPUT"],
              !outputPath.isEmpty
        else {
            return
        }

        let snapshot = try Self.sampleSnapshot()
        let model = ScribaBarModel()
        model.snapshot = snapshot
        model.state = .ready
        model.cliDescription = "system 0.0.0-alpha.0"
        model.lastUpdated = snapshot.generatedDate
        model.selectedProviderID = "codex"

        let view = NativeMenuSurfaceView(model: model)
            .environment(\.colorScheme, .dark)
        let hostingView = NSHostingView(rootView: view)
        hostingView.frame = NSRect(x: 0, y: 0, width: 310, height: 326)
        hostingView.layoutSubtreeIfNeeded()

        let bitmap = try #require(hostingView.bitmapImageRepForCachingDisplay(in: hostingView.bounds))
        hostingView.cacheDisplay(in: hostingView.bounds, to: bitmap)
        let png = try #require(bitmap.representation(using: .png, properties: [:]))

        let outputURL = URL(fileURLWithPath: outputPath)
        try FileManager.default.createDirectory(at: outputURL.deletingLastPathComponent(), withIntermediateDirectories: true)
        try png.write(to: outputURL)
    }

    private static func sampleSnapshot() throws -> StatusSnapshot {
        let json = """
        {
          "schemaVersion": "scriba.alpha.v1",
          "generatedAt": "2026-05-16T08:45:48.352Z",
          "providers": [
            {
              "providerId": "codex",
              "displayName": "Codex",
              "state": "ok",
              "lines": [
                { "type": "badge", "label": "Plan", "text": "prolite" },
                { "type": "progress", "label": "5h limit", "used": 4, "limit": 100, "resetsAt": "2026-05-16T12:46:31.000Z" },
                { "type": "progress", "label": "Weekly limit", "used": 24, "limit": 100, "resetsAt": "2026-05-19T07:10:35.000Z" },
                { "type": "progress", "label": "Spark weekly", "used": 24, "limit": 100, "resetsAt": "2026-05-19T07:10:35.000Z" },
                { "type": "progress", "label": "Spark 5h", "used": 4, "limit": 100, "resetsAt": "2026-05-16T12:46:31.000Z" },
                { "type": "text", "label": "Today", "value": "74,666,001" },
                { "type": "text", "label": "Yesterday", "value": "124,298,232" },
                { "type": "text", "label": "Last 30 Days", "value": "4,291,382,914" }
              ]
            },
            {
              "providerId": "claude",
              "displayName": "Claude",
              "state": "ok",
              "lines": [
                { "type": "badge", "label": "Claude API", "text": "Auth unavailable" },
                { "type": "text", "label": "Today", "value": "0" },
                { "type": "text", "label": "Yesterday", "value": "0" },
                { "type": "text", "label": "Last 30 Days", "value": "583,596,394" }
              ]
            }
          ]
        }
        """
        return try JSONDecoder().decode(StatusSnapshot.self, from: #require(json.data(using: .utf8)))
    }
}
