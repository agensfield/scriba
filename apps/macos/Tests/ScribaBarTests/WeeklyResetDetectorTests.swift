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
        #expect(events.first?.title == "Tibo just reset limits!")
        #expect(Int(events.first?.previousUsedPercent.rounded() ?? -1) == 28)
        #expect(Int(events.first?.currentUsedPercent.rounded() ?? -1) == 1)
    }

    @Test("alerts when 5h usage drops before the expected reset")
    func detectsFiveHourEarlyUsageDrop() {
        var detector = WeeklyResetDetector()
        _ = detector.observe(
            snapshot: snapshot(
                label: "5h limit",
                used: 41,
                resetsAt: "2026-05-16T15:00:00.000Z",
                periodDurationMs: 18_000_000),
            now: date("2026-05-16T10:00:00.000Z"))

        let events = detector.observe(
            snapshot: snapshot(
                label: "5h limit",
                used: 2,
                resetsAt: "2026-05-16T20:00:00.000Z",
                periodDurationMs: 18_000_000),
            now: date("2026-05-16T10:10:00.000Z"))

        #expect(events.count == 1)
        #expect(events.first?.providerID == "codex")
        #expect(events.first?.label == "5h limit")
    }

    @Test("does not alert on small 5h drift")
    func ignoresSmallFiveHourDrift() {
        var detector = WeeklyResetDetector()
        _ = detector.observe(
            snapshot: snapshot(
                label: "5h limit",
                used: 18,
                resetsAt: "2026-05-16T15:00:00.000Z",
                periodDurationMs: 18_000_000),
            now: date("2026-05-16T10:00:00.000Z"))

        let events = detector.observe(
            snapshot: snapshot(
                label: "5h limit",
                used: 14,
                resetsAt: "2026-05-16T15:00:00.000Z",
                periodDurationMs: 18_000_000),
            now: date("2026-05-16T10:10:00.000Z"))

        #expect(events.isEmpty)
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

    @Test("monitor does not emit the same reset twice")
    func monitorDeduplicatesResetEvents() {
        let suiteName = "ScribaBarWeeklyResetMonitorTests.\(UUID().uuidString)"
        let defaults = UserDefaults(suiteName: suiteName)!
        defer { defaults.removePersistentDomain(forName: suiteName) }
        let monitor = WeeklyResetMonitor(defaults: defaults)

        _ = monitor.observe(
            snapshot: snapshot(weeklyUsed: 82, resetsAt: "2026-05-19T07:10:35.000Z"),
            now: date("2026-05-16T10:00:00.000Z"))
        let resetSnapshot = snapshot(weeklyUsed: 2, resetsAt: "2026-05-23T10:00:00.000Z")

        let first = monitor.observe(
            snapshot: resetSnapshot,
            now: date("2026-05-16T12:00:00.000Z"))
        let second = monitor.observe(
            snapshot: resetSnapshot,
            now: date("2026-05-16T12:01:00.000Z"))

        #expect(first.count == 1)
        #expect(second.isEmpty)
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
                            resetsAt: resetsAt,
                            periodDurationMs: 604_800_000),
                    ]),
            ])
    }

    private func snapshot(label: String, used: Double, resetsAt: String, periodDurationMs: Double) -> StatusSnapshot {
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
                            label: label,
                            text: nil,
                            value: nil,
                            used: used,
                            limit: 100,
                            resetsAt: resetsAt,
                            periodDurationMs: periodDurationMs),
                    ]),
            ])
    }

    private func date(_ text: String) -> Date {
        ScribaDateParser.date(from: text)!
    }
}
