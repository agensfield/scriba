import Foundation
import Testing
@testable import ScribaBar

@Suite("status presentation")
struct StatusPresentationTests {
    @Test("overview extracts codex windows and claude local rollup")
    func overviewExtraction() throws {
        let json = """
        {
          "schemaVersion": "scriba.alpha.v1",
          "generatedAt": "2026-05-16T08:45:48.352Z",
          "providers": [
            {
              "providerId": "claude",
              "displayName": "Claude",
              "state": "ok",
              "lines": [
                { "type": "text", "label": "Last 30 Days", "value": "583,596,394" }
              ]
            },
            {
              "providerId": "codex",
              "displayName": "Codex",
              "state": "ok",
              "lines": [
                { "type": "badge", "label": "Plan", "text": "prolite" },
                { "type": "progress", "label": "5h limit", "used": 4, "limit": 100 },
                { "type": "progress", "label": "Weekly limit", "used": 24, "limit": 100 },
                { "type": "progress", "label": "Spark weekly", "used": 24, "limit": 100 }
              ]
            }
          ]
        }
        """
        let snapshot = try JSONDecoder().decode(StatusSnapshot.self, from: #require(json.data(using: .utf8)))

        #expect(snapshot.generatedDate != nil)
        #expect(snapshot.overview.codexFiveHour?.usedPercent == 4)
        #expect(snapshot.overview.codexWeekly?.usedPercent == 24)
        #expect(snapshot.overview.claudeThirtyDay == "583,596,394")
        #expect(snapshot.presentations.first { $0.id == "codex" }?.badgeText == "prolite")
    }
}
