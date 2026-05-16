import Foundation
import Testing
@testable import ScribaBar

@Suite("weekly reset detector")
struct WeeklyResetDetectorTests {
    @Test("does not alert on first weekly observation")
    func firstObservationOnlySeedsBaseline() {
        var detector = WeeklyResetDetector()
        let events = detector.observe(
            snapshot: snapshot(weeklyUsed: 28, resetsAt: "2026-05-19T07:10:35.000Z"),
            now: date("2026-05-16T10:00:00.000Z"))

        #expect(events.isEmpty)
    }

    @Test("alerts when weekly usage drops before the expected reset")
    func detectsEarlyUsageDrop() {
        var detector = WeeklyResetDetector()
        _ = detector.observe(
            snapshot: snapshot(weeklyUsed: 28, resetsAt: "2026-05-19T07:10:35.000Z"),
            now: date("2026-05-16T10:00:00.000Z"))

        let events = detector.observe(
            snapshot: snapshot(weeklyUsed: 1, resetsAt: "2026-05-23T10:00:00.000Z"),
            now: date("2026-05-16T12:00:00.000Z"))

        #expect(events.count == 1)
        #expect(events.first?.providerID == "codex")
        #expect(events.first?.label == "Weekly limit")
        #expect(Int(events.first?.previousUsedPercent.rounded() ?? -1) == 28)
        #expect(Int(events.first?.currentUsedPercent.rounded() ?? -1) == 1)
    }

    @Test("does not alert after the expected reset time has already arrived")
    func ignoresNormalResetWindow() {
        var detector = WeeklyResetDetector()
        _ = detector.observe(
            snapshot: snapshot(weeklyUsed: 80, resetsAt: "2026-05-19T07:10:35.000Z"),
            now: date("2026-05-16T10:00:00.000Z"))

        let events = detector.observe(
            snapshot: snapshot(weeklyUsed: 2, resetsAt: "2026-05-26T07:10:35.000Z"),
            now: date("2026-05-19T07:30:00.000Z"))

        #expect(events.isEmpty)
    }

    @Test("does not alert on small weekly drift")
    func ignoresSmallDrift() {
        var detector = WeeklyResetDetector()
        _ = detector.observe(
            snapshot: snapshot(weeklyUsed: 28, resetsAt: "2026-05-19T07:10:35.000Z"),
            now: date("2026-05-16T10:00:00.000Z"))

        let events = detector.observe(
            snapshot: snapshot(weeklyUsed: 24, resetsAt: "2026-05-19T07:10:35.000Z"),
            now: date("2026-05-16T12:00:00.000Z"))

        #expect(events.isEmpty)
    }

    private func snapshot(weeklyUsed: Double, resetsAt: String) -> StatusSnapshot {
        StatusSnapshot(
            schemaVersion: "scriba.alpha.v1",
            generatedAt: "2026-05-16T10:00:00.000Z",
            providers: [
                ProviderSnapshot(
                    providerId: "codex",
                    displayName: "Codex",
                    state: "ok",
                    lines: [
                        StatusLine(
                            type: "progress",
                            label: "Weekly limit",
                            text: nil,
                            value: nil,
                            used: weeklyUsed,
                            limit: 100,
                            resetsAt: resetsAt),
                    ]),
            ])
    }

    private func date(_ text: String) -> Date {
        ScribaDateParser.date(from: text)!
    }
}
