import AppKit
import SwiftUI

enum MenuPalette {
    static let primary = Color(nsColor: .controlTextColor)
    static let secondary = Color(nsColor: .secondaryLabelColor)
    static let separator = Color(nsColor: .separatorColor).opacity(0.50)
    static let selectedText = Color(nsColor: .selectedMenuItemTextColor)
    static let selectedBackground = Color(nsColor: .selectedContentBackgroundColor)
}
