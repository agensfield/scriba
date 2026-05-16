import AppKit
import SwiftUI

struct MenuActionRow: View {
    let title: String
    let systemImage: String
    let shortcut: String?
    let width: CGFloat
    let showsSubmenu: Bool
    let isEnabled: Bool
    let action: (() -> Void)?

    @State private var isHovered = false

    init(
        title: String,
        systemImage: String,
        shortcut: String? = nil,
        width: CGFloat,
        showsSubmenu: Bool = false,
        isEnabled: Bool = true,
        action: (() -> Void)? = nil)
    {
        self.title = title
        self.systemImage = systemImage
        self.shortcut = shortcut
        self.width = width
        self.showsSubmenu = showsSubmenu
        self.isEnabled = isEnabled
        self.action = action
    }

    var body: some View {
        Group {
            if let action {
                Button {
                    guard isEnabled else { return }
                    action()
                } label: {
                    rowBody
                }
                .buttonStyle(.plain)
                .disabled(!isEnabled)
            } else {
                rowBody
                    .opacity(isEnabled ? 1 : 0.58)
            }
        }
        .onHover { isHovered = $0 }
    }

    private var rowBody: some View {
        rowContent
            .frame(width: width, height: 29, alignment: .leading)
            .background(MenuRowBackground(isHighlighted: isHovered && isEnabled))
            .contentShape(Rectangle())
    }

    private var rowContent: some View {
        HStack(spacing: 9) {
            Image(systemName: systemImage)
                .font(.system(size: 13, weight: .semibold))
                .frame(width: 16, height: 16)
            Text(title)
                .font(.system(size: 13, weight: .medium))
            Spacer()
            if let shortcut {
                Text(shortcut)
                    .font(.system(size: 12, weight: .medium))
                    .foregroundStyle(secondaryColor)
            }
            if showsSubmenu {
                Image(systemName: "chevron.right")
                    .font(.system(size: 11, weight: .bold))
                    .foregroundStyle(secondaryColor)
            }
        }
        .foregroundStyle(primaryColor)
        .padding(.horizontal, 14)
        .opacity(isEnabled ? 1 : 0.58)
    }

    private var primaryColor: Color {
        isHovered && isEnabled ? MenuPalette.selectedText : MenuPalette.primary
    }

    private var secondaryColor: Color {
        isHovered && isEnabled ? MenuPalette.selectedText.opacity(0.82) : MenuPalette.secondary.opacity(0.75)
    }
}

struct MenuDividerRow: View {
    let width: CGFloat

    var body: some View {
        Color.clear
            .frame(width: width, height: 9)
            .overlay(alignment: .center) {
                Rectangle()
                    .fill(MenuPalette.separator)
                    .frame(height: 1)
                    .padding(.horizontal, 14)
            }
    }
}

struct MenuFooterBackground: View {
    var body: some View {
        Color.clear
    }
}

struct MenuRowBackground: View {
    let isHighlighted: Bool

    var body: some View {
        Color.clear
            .overlay {
                if isHighlighted {
                    RoundedRectangle(cornerRadius: 6, style: .continuous)
                        .fill(MenuPalette.selectedBackground)
                        .padding(.horizontal, 6)
                        .padding(.vertical, 2)
                }
            }
    }
}
