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
        "Tibo just reset limits!"
    }

    var message: String {
        let previous = Self.percent(previousUsedPercent)
        let current = Self.percent(currentUsedPercent)
        let expected = previousResetAt.formatted(date: .abbreviated, time: .shortened)
        if let currentResetAt {
            let next = currentResetAt.formatted(date: .abbreviated, time: .shortened)
            return "\(providerName) \(label) dropped from \(previous) to \(current) before \(expected). Next window now lands \(next). Go be irresponsible."
        }
        return "\(providerName) \(label) dropped from \(previous) to \(current) before \(expected). Go be irresponsible."
    }

    var telegramMessage: String {
        "🎉 Tibo just reset limits! 🎊\n\(message)"
    }

    private static func percent(_ value: Double) -> String {
        "\(Int(value.rounded()))%"
    }
}

struct WeeklyResetDetector {
    private static let maximumEarlyResetGrace: TimeInterval = 10 * 60
    private static let fallbackEarlyResetGrace: TimeInterval = 10 * 60
    private static let minimumForwardJump: TimeInterval = 30 * 60

    private var baselines: [String: WeeklyLimitObservation]

    init(baselines: [String: WeeklyLimitObservation] = [:]) {
        self.baselines = baselines
    }

    mutating func observe(snapshot: StatusSnapshot, now: Date = Date()) -> [WeeklyLimitResetEvent] {
        var events: [WeeklyLimitResetEvent] = []
        for observation in Self.resetLimitObservations(in: snapshot) {
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
              previousResetAt.timeIntervalSince(now) > Self.earlyResetGrace(for: previous)
        else {
            return nil
        }

        let drop = previous.usedPercent - current.usedPercent
        let usageDropLooksLikeReset = drop >= Self.minimumResetDrop(for: previous) &&
            current.usedPercent <= max(Self.lowWatermark(for: previous), previous.usedPercent * 0.62)
        let resetWindowMovedForward = current.resetAt.map {
            $0.timeIntervalSince(previousResetAt) > Self.resetWindowForwardJump(for: previous)
        } ?? false
        let resetWindowMovedBackward = current.resetAt.map {
            previousResetAt.timeIntervalSince($0) > Self.resetWindowForwardJump(for: previous)
        } ?? false

        guard usageDropLooksLikeReset ||
            (resetWindowMovedForward && drop >= Self.minimumForwardJumpDrop(for: previous)) ||
            (resetWindowMovedBackward && current.usedPercent <= Self.lowWatermark(for: previous))
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

    private static func resetLimitObservations(in snapshot: StatusSnapshot) -> [WeeklyLimitObservation] {
        snapshot.providers.flatMap { provider in
            provider.lines.compactMap { line -> WeeklyLimitObservation? in
                guard line.type == "progress",
                      Self.isResetLimit(line),
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
                    resetAt: line.resetsAt.flatMap(ScribaDateParser.date(from:)),
                    periodDurationMs: line.periodDurationMs)
            }
        }
    }

    private static func isResetLimit(_ line: StatusLine) -> Bool {
        let label = line.label.lowercased()
        if label.contains("weekly") || label.contains("5h") {
            return true
        }
        guard let duration = line.periodDurationMs else { return false }
        let hours = duration / 3_600_000
        return hours >= 4.5 && hours <= 5.5 || hours >= 160 && hours <= 170
    }

    private static func earlyResetGrace(for observation: WeeklyLimitObservation) -> TimeInterval {
        guard let duration = observation.periodDuration else {
            return fallbackEarlyResetGrace
        }
        return min(maximumEarlyResetGrace, max(2 * 60, duration * 0.04))
    }

    private static func resetWindowForwardJump(for observation: WeeklyLimitObservation) -> TimeInterval {
        guard let duration = observation.periodDuration else {
            return 60 * 60
        }
        return max(minimumForwardJump, min(6 * 60 * 60, duration * 0.20))
    }

    private static func minimumResetDrop(for observation: WeeklyLimitObservation) -> Double {
        observation.isShortWindow ? 8 : 12
    }

    private static func minimumForwardJumpDrop(for observation: WeeklyLimitObservation) -> Double {
        observation.isShortWindow ? 3 : 5
    }

    private static func lowWatermark(for observation: WeeklyLimitObservation) -> Double {
        observation.isShortWindow ? 8 : 10
    }
}

struct WeeklyLimitObservation: Codable, Equatable {
    let providerID: String
    let providerName: String
    let label: String
    let usedPercent: Double
    let resetAt: Date?
    let periodDurationMs: Double?

    var key: String {
        "\(providerID):\(label.lowercased())"
    }

    var periodDuration: TimeInterval? {
        periodDurationMs.map { $0 / 1_000 }
    }

    var isShortWindow: Bool {
        guard let periodDuration else {
            return label.localizedCaseInsensitiveContains("5h")
        }
        return periodDuration <= 6 * 60 * 60
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
