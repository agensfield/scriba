import SwiftUI

extension View {
    @ViewBuilder
    func scribaButtonStyle(prominent: Bool = false) -> some View {
        self.buttonStyle(ScribaPillButtonStyle(prominent: prominent))
    }
}

private struct ScribaPillButtonStyle: ButtonStyle {
    let prominent: Bool

    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .font(.callout.weight(.semibold))
            .foregroundStyle(prominent ? Color.white : MenuPalette.primary)
            .padding(.horizontal, 14)
            .frame(height: 34)
            .background {
                RoundedRectangle(cornerRadius: 12, style: .continuous)
                    .fill(prominent ? AnyShapeStyle(MenuPalette.codex.gradient) : AnyShapeStyle(.regularMaterial))
                    .overlay {
                        RoundedRectangle(cornerRadius: 12, style: .continuous)
                            .stroke(.white.opacity(prominent ? 0.18 : 0.10), lineWidth: 1)
                    }
                    .shadow(color: .black.opacity(prominent ? 0.22 : 0.10), radius: 10, y: 5)
            }
            .scaleEffect(configuration.isPressed ? 0.98 : 1)
            .opacity(configuration.isPressed ? 0.82 : 1)
    }
}
