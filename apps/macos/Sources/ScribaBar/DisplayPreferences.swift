import Foundation

enum UsagePercentMode: String, CaseIterable, Identifiable {
    case used
    case remaining

    var id: String { rawValue }

    var label: String {
        switch self {
        case .used:
            "Used"
        case .remaining:
            "Remaining"
        }
    }

    var suffix: String {
        switch self {
        case .used:
            "used"
        case .remaining:
            "left"
        }
    }
}

enum MenuBarTextMode: String, CaseIterable, Identifiable {
    case iconOnly
    case percent
    case pace
    case both

    var id: String { rawValue }

    var label: String {
        switch self {
        case .iconOnly:
            "Icon"
        case .percent:
            "Percent"
        case .pace:
            "Pace"
        case .both:
            "Both"
        }
    }
}

enum RefreshCadence: String, CaseIterable, Identifiable {
    case oneMinute
    case twoMinutes
    case fiveMinutes
    case tenMinutes
    case fifteenMinutes

    var id: String { rawValue }

    var label: String {
        switch self {
        case .oneMinute:
            "1m"
        case .twoMinutes:
            "2m"
        case .fiveMinutes:
            "5m"
        case .tenMinutes:
            "10m"
        case .fifteenMinutes:
            "15m"
        }
    }

    var nanoseconds: UInt64 {
        switch self {
        case .oneMinute:
            60 * 1_000_000_000
        case .twoMinutes:
            2 * 60 * 1_000_000_000
        case .fiveMinutes:
            5 * 60 * 1_000_000_000
        case .tenMinutes:
            10 * 60 * 1_000_000_000
        case .fifteenMinutes:
            15 * 60 * 1_000_000_000
        }
    }
}

enum ScribaBarDefaults {
    static let usagePercentMode = "ScribaBar.UsagePercentMode"
    static let menuBarTextMode = "ScribaBar.MenuBarTextMode"
    static let refreshCadence = "ScribaBar.RefreshCadence"
    static let menuVisibilityGuidanceShown = "ScribaBar.MenuVisibilityGuidanceShown"
    static let menuVisibilityGuidanceLastShownAt = "ScribaBar.MenuVisibilityGuidanceLastShownAt"
}
