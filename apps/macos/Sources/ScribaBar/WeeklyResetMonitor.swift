import Foundation

struct WeeklyLimitResetEvent: Equatable {
    let providerID: String
    let providerName: String
    let label: String
    let previousUsedPercent: Double
    let currentUsedPercent: Double
    let previousResetAt: Date
    let currentResetAt: Date?

    var title: String {
        "\(providerName) weekly limit reset early"
    }

    var message: String {
        let previous = Self.percent(previousUsedPercent)
        let current = Self.percent(currentUsedPercent)
        let expected = previousResetAt.formatted(date: .abbreviated, time: .shortened)
        if let currentResetAt {
            let next = currentResetAt.formatted(date: .abbreviated, time: .shortened)
            return "\(label) dropped from \(previous) to \(current) before \(expected). Next reset now shows \(next)."
        }
        return "\(label) dropped from \(previous) to \(current) before \(expected)."
    }

    private static func percent(_ value: Double) -> String {
        "\(Int(value.rounded()))%"
    }
}

struct WeeklyResetDetector {
    private static let earlyResetGrace: TimeInterval = 10 * 60
    private static let resetWindowForwardJump: TimeInterval = 60 * 60
    private static let minimumResetDrop = 15.0
    private static let minimumForwardJumpDrop = 5.0

    private var baselines: [String: WeeklyLimitObservation]

    init(baselines: [String: WeeklyLimitObservation] = [:]) {
        self.baselines = baselines
    }

    mutating func observe(snapshot: StatusSnapshot, now: Date = Date()) -> [WeeklyLimitResetEvent] {
        var events: [WeeklyLimitResetEvent] = []
        for observation in Self.weeklyObservations(in: snapshot) {
            if let previous = baselines[observation.key],
               let event = Self.event(previous: previous, current: observation, now: now)
            {
                events.append(event)
            }
            baselines[observation.key] = observation
        }
        return events
    }

    var snapshotBaselines: [String: WeeklyLimitObservation] {
        baselines
    }

    private static func event(
        previous: WeeklyLimitObservation,
        current: WeeklyLimitObservation,
        now: Date)
        -> WeeklyLimitResetEvent?
    {
        guard let previousResetAt = previous.resetAt,
              previousResetAt.timeIntervalSince(now) > earlyResetGrace
        else {
            return nil
        }

        let drop = previous.usedPercent - current.usedPercent
        let usageDropLooksLikeReset = drop >= minimumResetDrop &&
            current.usedPercent <= max(10, previous.usedPercent * 0.55)
        let resetWindowMovedForward = current.resetAt.map {
            $0.timeIntervalSince(previousResetAt) > resetWindowForwardJump
        } ?? false

        guard usageDropLooksLikeReset ||
            (resetWindowMovedForward && drop >= minimumForwardJumpDrop)
        else {
            return nil
        }

        return WeeklyLimitResetEvent(
            providerID: current.providerID,
            providerName: current.providerName,
            label: current.label,
            previousUsedPercent: previous.usedPercent,
            currentUsedPercent: current.usedPercent,
            previousResetAt: previousResetAt,
            currentResetAt: current.resetAt)
    }

    private static func weeklyObservations(in snapshot: StatusSnapshot) -> [WeeklyLimitObservation] {
        snapshot.providers.flatMap { provider in
            provider.lines.compactMap { line -> WeeklyLimitObservation? in
                guard line.type == "progress",
                      line.label.localizedCaseInsensitiveContains("weekly"),
                      let used = line.used
                else {
                    return nil
                }

                let limit = max(line.limit ?? 100, 1)
                let usedPercent = min(max((used / limit) * 100, 0), 100)
                return WeeklyLimitObservation(
                    providerID: provider.providerId,
                    providerName: provider.displayName,
                    label: line.label,
                    usedPercent: usedPercent,
                    resetAt: line.resetsAt.flatMap(ScribaDateParser.date(from:)))
            }
        }
    }
}

struct WeeklyLimitObservation: Codable, Equatable {
    let providerID: String
    let providerName: String
    let label: String
    let usedPercent: Double
    let resetAt: Date?

    var key: String {
        "\(providerID):\(label.lowercased())"
    }
}

final class WeeklyResetMonitor {
    private let defaults: UserDefaults
    private let storageKey = "ScribaBar.WeeklyResetBaselines"
    private var detector: WeeklyResetDetector

    init(defaults: UserDefaults = .standard) {
        self.defaults = defaults
        self.detector = WeeklyResetDetector(baselines: Self.loadBaselines(from: defaults, key: storageKey))
    }

    func observe(snapshot: StatusSnapshot, now: Date = Date()) -> [WeeklyLimitResetEvent] {
        let events = detector.observe(snapshot: snapshot, now: now)
        saveBaselines(detector.snapshotBaselines)
        return events
    }

    private func saveBaselines(_ baselines: [String: WeeklyLimitObservation]) {
        guard let data = try? JSONEncoder().encode(baselines) else { return }
        defaults.set(data, forKey: storageKey)
    }

    private static func loadBaselines(
        from defaults: UserDefaults,
        key: String)
        -> [String: WeeklyLimitObservation]
    {
        guard let data = defaults.data(forKey: key),
              let baselines = try? JSONDecoder().decode([String: WeeklyLimitObservation].self, from: data)
        else {
            return [:]
        }
        return baselines
    }
}
