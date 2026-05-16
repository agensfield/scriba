import Foundation

struct UsageHistoryMetric: Codable, Equatable, Identifiable {
    var id: String { label }

    let label: String
    let usedPercent: Double
    let resetAt: Date?
}

struct UsageHistoryEntry: Codable, Equatable, Identifiable {
    var id: String { "\(providerID):\(recordedAt.timeIntervalSince1970)" }

    let recordedAt: Date
    let providerID: String
    let providerName: String
    let metrics: [UsageHistoryMetric]
}

final class UsageHistoryStore {
    private static let defaultKey = "ScribaBar.UsageHistory.v1"

    private let defaults: UserDefaults
    private let key: String
    private let maxEntriesPerProvider: Int
    private let minimumSpacing: TimeInterval

    init(
        defaults: UserDefaults = .standard,
        key: String = UsageHistoryStore.defaultKey,
        maxEntriesPerProvider: Int = 60,
        minimumSpacing: TimeInterval = 60)
    {
        self.defaults = defaults
        self.key = key
        self.maxEntriesPerProvider = maxEntriesPerProvider
        self.minimumSpacing = minimumSpacing
    }

    func load() -> [String: [UsageHistoryEntry]] {
        guard let data = defaults.data(forKey: key) else { return [:] }
        return (try? JSONDecoder().decode([String: [UsageHistoryEntry]].self, from: data)) ?? [:]
    }

    @discardableResult
    func observe(snapshot: StatusSnapshot, now: Date = Date()) -> [String: [UsageHistoryEntry]] {
        var history = load()

        for provider in snapshot.providers {
            let metrics = Self.metrics(from: provider)
            guard !metrics.isEmpty else { continue }

            let entry = UsageHistoryEntry(
                recordedAt: now,
                providerID: provider.providerId,
                providerName: provider.displayName,
                metrics: metrics)

            var entries = history[provider.providerId] ?? []
            if Self.shouldAppend(entry, after: entries.last, minimumSpacing: minimumSpacing) {
                entries.append(entry)
                if entries.count > maxEntriesPerProvider {
                    entries = Array(entries.suffix(maxEntriesPerProvider))
                }
                history[provider.providerId] = entries
            }
        }

        save(history)
        return history
    }

    private func save(_ history: [String: [UsageHistoryEntry]]) {
        guard let data = try? JSONEncoder().encode(history) else { return }
        defaults.set(data, forKey: key)
    }

    private static func metrics(from provider: ProviderSnapshot) -> [UsageHistoryMetric] {
        provider.lines.compactMap { line in
            guard line.type == "progress", let used = line.used else { return nil }
            let limit = max(line.limit ?? 100, 1)
            let percent = min(max((used / limit) * 100, 0), 100)
            return UsageHistoryMetric(
                label: line.label,
                usedPercent: percent,
                resetAt: line.resetsAt.flatMap(ScribaDateParser.date(from:)))
        }
    }

    private static func shouldAppend(
        _ entry: UsageHistoryEntry,
        after previous: UsageHistoryEntry?,
        minimumSpacing: TimeInterval)
        -> Bool
    {
        guard let previous else { return true }
        if entry.recordedAt.timeIntervalSince(previous.recordedAt) >= minimumSpacing {
            return true
        }

        let previousByLabel = Dictionary(uniqueKeysWithValues: previous.metrics.map { ($0.label, $0) })
        let currentLabels = Set(entry.metrics.map(\.label))
        guard currentLabels == Set(previousByLabel.keys) else { return true }

        return entry.metrics.contains { metric in
            guard let previousMetric = previousByLabel[metric.label] else { return true }
            if abs(previousMetric.usedPercent - metric.usedPercent) >= 0.1 {
                return true
            }
            return previousMetric.resetAt != metric.resetAt
        }
    }
}
