import SwiftUI

struct RefreshMenuRow: View {
    @ObservedObject var model: ScribaBarModel
    let width: CGFloat
    @State private var isHovered = false

    var body: some View {
        Button {
            guard !model.isRefreshing else { return }
            Task {
                await model.refresh()
            }
        } label: {
            HStack(spacing: 9) {
                if model.isRefreshing {
                    ProgressView()
                        .controlSize(.small)
                        .frame(width: 16, height: 16)
                } else {
                    Image(systemName: "arrow.clockwise")
                        .font(.system(size: 13, weight: .semibold))
                        .frame(width: 16, height: 16)
                }
                Text("Refresh")
                    .font(.system(size: 13, weight: .medium))
                Spacer()
                if model.isRefreshing {
                    Text("working")
                        .font(.system(size: 10, weight: .semibold))
                        .foregroundStyle(MenuPalette.secondary.opacity(0.72))
                }
                Text("⌘ R")
                    .font(.system(size: 12, weight: .medium))
                    .foregroundStyle(secondaryColor)
            }
            .foregroundStyle(primaryColor)
            .padding(.horizontal, 14)
            .frame(width: width, height: 29, alignment: .leading)
            .background(MenuRowBackground(isHighlighted: isHovered && !model.isRefreshing))
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .disabled(model.isRefreshing)
        .opacity(1)
        .onHover { isHovered = $0 }
    }

    private var primaryColor: Color {
        isHovered && !model.isRefreshing ? MenuPalette.selectedText : MenuPalette.primary
    }

    private var secondaryColor: Color {
        isHovered && !model.isRefreshing ? MenuPalette.selectedText.opacity(0.82) : MenuPalette.secondary.opacity(0.75)
    }
}
