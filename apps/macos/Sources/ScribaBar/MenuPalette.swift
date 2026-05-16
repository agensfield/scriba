import AppKit
import SwiftUI

enum MenuPalette {
    static let primary = Color(nsColor: .controlTextColor)
    static let secondary = Color(nsColor: .secondaryLabelColor)
    static let separator = Color(nsColor: .separatorColor).opacity(0.50)
    static let selectedText = Color(nsColor: .selectedMenuItemTextColor)
    static let selectedBackground = Color(nsColor: .selectedContentBackgroundColor)
    static let codex = Color(red: 0.16, green: 0.72, blue: 0.80)
    static let claude = Color(red: 0.94, green: 0.52, blue: 0.30)
    static let graphite = Color(nsColor: .tertiaryLabelColor)

    static func accent(for providerID: String?) -> Color {
        switch providerID {
        case "codex":
            codex
        case "claude":
            claude
        default:
            Color(nsColor: .controlAccentColor)
        }
    }
}
