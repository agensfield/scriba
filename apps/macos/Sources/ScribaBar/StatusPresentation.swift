import Foundation
import SwiftUI

struct ProviderPresentation: Identifiable {
    let id: String
    let provider: ProviderSnapshot
    let badgeText: String?
    let headlineValue: String?
    let headlineLabel: String?
    let metrics: [MetricPresentation]
    let textLines: [StatusLine]

    var displayName: String {
        provider.displayName
    }

    var stateLabel: String {
        switch provider.state.lowercased() {
        case "ok":
            return "Live"
        case "stale":
            return "Stale"
        default:
            return provider.state.capitalized
        }
    }

    var stateColor: Color {
        switch provider.state.lowercased() {
        case "ok":
            return .green
        case "stale":
            return .orange
        default:
            return .red
        }
    }
}

struct MetricPresentation: Identifiable {
    let id: String
    let label: String
    let usedPercent: Double
    let resetText: String?

    var remainingPercent: Double {
        100 - usedPercent
    }

    var percentLabel: String {
        "\(Int(usedPercent.rounded()))% used"
    }
}

struct StatusOverview {
    let codexFiveHour: MetricPresentation?
    let codexWeekly: MetricPresentation?
    let claudeThirtyDay: String?
}

extension StatusSnapshot {
    var generatedDate: Date? {
        ScribaDateParser.date(from: generatedAt)
    }

    var presentations: [ProviderPresentation] {
        providers.map { provider in
            let badge = provider.lines.first { $0.type == "badge" }?.displayValue
            let metrics = provider.lines
                .filter { $0.type == "progress" }
                .compactMap(MetricPresentation.init(line:))
            let textLines = provider.lines.filter { $0.type != "progress" && $0.type != "badge" }
            let headline = provider.lines.first { $0.label.lowercased() == "today" }
                ?? provider.lines.first { $0.type == "text" }

            return ProviderPresentation(
                id: provider.providerId,
                provider: provider,
                badgeText: badge,
                headlineValue: headline?.displayValue,
                headlineLabel: headline?.label,
                metrics: metrics,
                textLines: textLines)
        }
    }

    var overview: StatusOverview {
        let codex = providers.first { $0.providerId == "codex" }
        let claude = providers.first { $0.providerId == "claude" }
        return StatusOverview(
            codexFiveHour: codex?.lines
                .first { $0.type == "progress" && $0.label.localizedCaseInsensitiveContains("5h") }
                .flatMap(MetricPresentation.init(line:)),
            codexWeekly: codex?.lines
                .first {
                    $0.type == "progress"
                        && $0.label.localizedCaseInsensitiveContains("weekly")
                        && !$0.label.localizedCaseInsensitiveContains("spark")
                }
                .flatMap(MetricPresentation.init(line:)),
            claudeThirtyDay: claude?.lines
                .first { $0.label.localizedCaseInsensitiveContains("30") }?
                .displayValue)
    }
}

extension StatusLine {
    var displayValue: String? {
        text ?? value
    }
}

extension MetricPresentation {
    init?(line: StatusLine) {
        guard let used = line.used else { return nil }
        let limit = max(line.limit ?? 100, 1)
        let percent = min(max((used / limit) * 100, 0), 100)
        self.init(
            id: line.id,
            label: line.label,
            usedPercent: percent,
            resetText: line.resetDescription)
    }
}

private extension StatusLine {
    var resetDescription: String? {
        guard let resetsAt,
              let date = ScribaDateParser.date(from: resetsAt)
        else {
            return nil
        }
        return "resets \(date.formatted(.relative(presentation: .named)))"
    }
}

enum ScribaDateParser {
    static func date(from text: String) -> Date? {
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        if let date = formatter.date(from: text) {
            return date
        }

        formatter.formatOptions = [.withInternetDateTime]
        return formatter.date(from: text)
    }
}
