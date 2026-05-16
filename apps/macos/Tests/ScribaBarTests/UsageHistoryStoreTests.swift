import Foundation
import Testing
@testable import ScribaBar

@Suite("usage history store")
struct UsageHistoryStoreTests {
    @Test("stores progress samples per provider")
    func storesProgressSamples() {
        let defaults = UserDefaults(suiteName: "UsageHistoryStoreTests.\(UUID().uuidString)")!
        let store = UsageHistoryStore(defaults: defaults, key: "history", minimumSpacing: 60)

        let history = store.observe(
            snapshot: snapshot(weeklyUsed: 28, fiveHourUsed: 6),
            now: date("2026-05-16T10:00:00.000Z"))

        let entries = history["codex"] ?? []
        #expect(entries.count == 1)
        #expect(entries.first?.metrics.map(\.label).contains("Weekly limit") == true)
        let weeklyPercent = entries.first?.metrics.first(where: { $0.label == "Weekly limit" })?.usedPercent ?? -1
        #expect(abs(weeklyPercent - 28) < 0.01)
    }

    @Test("deduplicates unchanged samples inside minimum spacing")
    func deduplicatesUnchangedSamples() {
        let defaults = UserDefaults(suiteName: "UsageHistoryStoreTests.\(UUID().uuidString)")!
        let store = UsageHistoryStore(defaults: defaults, key: "history", minimumSpacing: 60)

        _ = store.observe(
            snapshot: snapshot(weeklyUsed: 28, fiveHourUsed: 6),
            now: date("2026-05-16T10:00:00.000Z"))
        let history = store.observe(
            snapshot: snapshot(weeklyUsed: 28, fiveHourUsed: 6),
            now: date("2026-05-16T10:00:20.000Z"))

        #expect(history["codex"]?.count == 1)
    }

    @Test("keeps changed samples even inside minimum spacing")
    func keepsChangedSamples() {
        let defaults = UserDefaults(suiteName: "UsageHistoryStoreTests.\(UUID().uuidString)")!
        let store = UsageHistoryStore(defaults: defaults, key: "history", minimumSpacing: 60)

        _ = store.observe(
            snapshot: snapshot(weeklyUsed: 28, fiveHourUsed: 6),
            now: date("2026-05-16T10:00:00.000Z"))
        let history = store.observe(
            snapshot: snapshot(weeklyUsed: 24, fiveHourUsed: 7),
            now: date("2026-05-16T10:00:20.000Z"))

        #expect(history["codex"]?.count == 2)
    }

    private func snapshot(weeklyUsed: Double, fiveHourUsed: Double) -> StatusSnapshot {
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
                            label: "5h limit",
                            text: nil,
                            value: nil,
                            used: fiveHourUsed,
                            limit: 100,
                            resetsAt: "2026-05-16T12:46:31.000Z"),
                        StatusLine(
                            type: "progress",
                            label: "Weekly limit",
                            text: nil,
                            value: nil,
                            used: weeklyUsed,
                            limit: 100,
                            resetsAt: "2026-05-19T07:10:35.000Z"),
                    ]),
            ])
    }

    private func date(_ text: String) -> Date {
        ScribaDateParser.date(from: text)!
    }
}
