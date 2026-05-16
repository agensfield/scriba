import AppKit
import SwiftUI

struct NativeMenuSurfaceView: View {
    @ObservedObject var model: ScribaBarModel
    private let width: CGFloat = 310
    private let height: CGFloat = 368

    private var presentations: [ProviderPresentation] {
        model.snapshot?.presentations ?? []
    }

    private var selectedProvider: ProviderPresentation? {
        guard model.selectedProviderID != "overview" else { return nil }
        return presentations.first { $0.id == model.selectedProviderID } ?? presentations.first
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            if let snapshot = model.snapshot {
                ProviderSwitcher(
                    providers: presentations,
                    overview: snapshot.overview,
                    selection: $model.selectedProviderID)
                Divider().overlay(MenuPalette.separator)
            }
            content
        }
        .frame(width: width, height: height, alignment: .top)
    }

    @ViewBuilder
    private var content: some View {
        switch model.state {
        case .loading:
            LoadingPane()
        case let .failed(message):
            FailurePane(message: message)
        case .ready:
            VStack(alignment: .leading, spacing: 12) {
                HeaderBlock(
                    provider: selectedProvider,
                    cliDescription: model.cliDescription,
                    generatedDate: model.snapshot?.generatedDate)
                if let selectedProvider {
                    HeroUsageCard(provider: selectedProvider, percentMode: model.usagePercentMode)
                } else if let snapshot = model.snapshot {
                    OverviewUsageCard(snapshot: snapshot, percentMode: model.usagePercentMode)
                }
            }
            .padding(.horizontal, 10)
            .padding(.vertical, 10)
        }
    }
}

private struct ProviderSwitcher: View {
    let providers: [ProviderPresentation]
    let overview: StatusOverview
    @Binding var selection: String

    var body: some View {
        LazyVGrid(columns: Array(repeating: GridItem(.flexible(), spacing: 6), count: 3), spacing: 6) {
            SwitcherButton(
                id: "overview",
                title: "Overview",
                subtitle: overview.codexWeekly?.percentLabel(mode: .used) ?? "usage",
                systemImage: "square.grid.2x2",
                isSelected: selection == "overview",
                action: { selection = "overview" })

            ForEach(providers) { provider in
                SwitcherButton(
                    id: provider.id,
                    title: provider.displayName,
                    subtitle: provider.badgeText ?? provider.stateLabel,
                    systemImage: symbol(for: provider.provider.providerId),
                    isSelected: selection == provider.id,
                    action: { selection = provider.id })
            }
        }
        .padding(.horizontal, 8)
        .padding(.vertical, 8)
    }

    private func symbol(for providerID: String) -> String {
        switch providerID {
        case "codex":
            return "terminal"
        case "claude":
            return "sparkles"
        default:
            return "circle.hexagongrid"
        }
    }
}

private struct SwitcherButton: View {
    let id: String
    let title: String
    let subtitle: String
    let systemImage: String
    let isSelected: Bool
    let action: () -> Void

    var body: some View {
        VStack(spacing: 2) {
            Image(systemName: systemImage)
                .font(.system(size: 13, weight: .semibold))
                .frame(height: 14)
            Text(title)
                .font(.system(size: 10.5, weight: .semibold))
                .lineLimit(1)
            Text(subtitle)
                .font(.system(size: 8.5, weight: .semibold))
                .foregroundStyle(isSelected ? MenuPalette.selectedText.opacity(0.86) : MenuPalette.secondary)
                .lineLimit(1)
                .minimumScaleFactor(0.7)
        }
        .foregroundStyle(isSelected ? MenuPalette.selectedText : MenuPalette.secondary)
        .frame(maxWidth: .infinity)
        .frame(height: 42)
        .background(selectionBackground)
        .overlay(
            RoundedRectangle(cornerRadius: 8, style: .continuous)
                .stroke(Color.white.opacity(isSelected ? 0.20 : 0.06), lineWidth: 1))
        .contentShape(RoundedRectangle(cornerRadius: 8, style: .continuous))
        .onTapGesture(perform: action)
    }

    @ViewBuilder
    private var selectionBackground: some View {
        if isSelected {
            LinearGradient(
                colors: [
                    MenuPalette.selectedBackground.opacity(0.94),
                    Color(red: 0.10, green: 0.48, blue: 0.55).opacity(0.82),
                ],
                startPoint: .topLeading,
                endPoint: .bottomTrailing)
                .clipShape(RoundedRectangle(cornerRadius: 8, style: .continuous))
        } else {
            RoundedRectangle(cornerRadius: 8, style: .continuous)
                .fill(Color(red: 0.075, green: 0.080, blue: 0.086))
        }
    }
}

private struct HeaderBlock: View {
    let provider: ProviderPresentation?
    let cliDescription: String
    let generatedDate: Date?

    var body: some View {
        HStack(alignment: .bottom, spacing: 10) {
            VStack(alignment: .leading, spacing: 3) {
                Text(provider?.displayName ?? "Scriba")
                    .font(.system(size: 17, weight: .bold))
                    .foregroundStyle(MenuPalette.primary)
                Text(generatedDate.map { "Updated \($0.formatted(.relative(presentation: .named)))" } ?? cliDescription)
                    .font(.system(size: 10, weight: .semibold))
                    .foregroundStyle(MenuPalette.secondary)
            }
            Spacer()
            Text(provider?.badgeText ?? cliDescription)
                .font(.system(size: 10, weight: .semibold))
                .foregroundStyle(MenuPalette.secondary)
                .lineLimit(1)
        }
    }
}

private struct HeroUsageCard: View {
    let provider: ProviderPresentation
    let percentMode: UsagePercentMode

    private var primaryMetric: MetricPresentation? {
        provider.metrics.first
    }

    private var secondaryMetric: MetricPresentation? {
        provider.metrics.dropFirst().first
    }

    private var today: String {
        provider.textLines.first { $0.label == "Today" }?.displayValue ?? "0"
    }

    private var lastThirty: String {
        provider.textLines.first { $0.label.localizedCaseInsensitiveContains("30") }?.displayValue ?? "0"
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack(alignment: .top) {
                VStack(alignment: .leading, spacing: 7) {
                    MetricPair(label: "Today", value: today)
                    MetricPair(label: "30d tokens", value: lastThirty)
                }

                Spacer()

                VStack(alignment: .leading, spacing: 7) {
                    MetricPair(
                        label: primaryMetric?.label ?? "Primary",
                        value: primaryMetric?.percentLabel(mode: percentMode) ?? "n/a")
                    MetricPair(
                        label: secondaryMetric?.label ?? "Secondary",
                        value: secondaryMetric?.percentLabel(mode: percentMode) ?? "n/a")
                }
            }

            Divider().overlay(MenuPalette.separator)

            UsageMeterStack(provider: provider, metrics: Array(provider.metrics.prefix(3)), percentMode: percentMode)
                .frame(height: 88)

            MiniBarChart(seed: provider.id, metrics: provider.metrics, percentMode: percentMode)
                .frame(height: 40)

            if !provider.metrics.isEmpty {
                UsageSummaryFooter(metrics: Array(provider.metrics.prefix(2)), percentMode: percentMode)
            }
        }
        .padding(.horizontal, 6)
        .padding(.vertical, 2)
    }
}

private struct OverviewUsageCard: View {
    let snapshot: StatusSnapshot
    let percentMode: UsagePercentMode

    private var codex: ProviderPresentation? {
        snapshot.presentations.first { $0.id == "codex" }
    }

    private var claude: ProviderPresentation? {
        snapshot.presentations.first { $0.id == "claude" }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack(spacing: 8) {
                OverviewTile(
                    title: "Codex",
                    value: snapshot.overview.codexWeekly?.percentLabel(mode: percentMode) ?? codex?.badgeText ?? "ready",
                    subtitle: snapshot.overview.codexFiveHour?.resetText ?? "5h window",
                    accent: MenuPalette.accent(for: "codex"))
                OverviewTile(
                    title: "Claude",
                    value: claude?.headlineValue ?? snapshot.overview.claudeThirtyDay ?? "ready",
                    subtitle: claude?.badgeText ?? "local logs",
                    accent: MenuPalette.accent(for: "claude"))
            }

            Divider().overlay(MenuPalette.separator)

            if let codex {
                UsageMeterStack(
                    provider: codex,
                    metrics: Array(codex.metrics.prefix(2)),
                    percentMode: percentMode)
                    .frame(height: 62)
            }

            if let claude {
                UsageMeterStack(
                    provider: claude,
                    metrics: Array(claude.metrics.prefix(2)),
                    percentMode: percentMode)
                    .frame(height: 62)
            }
        }
        .padding(.horizontal, 6)
        .padding(.vertical, 2)
    }
}

private struct OverviewTile: View {
    let title: String
    let value: String
    let subtitle: String
    let accent: Color

    var body: some View {
        VStack(alignment: .leading, spacing: 3) {
            Text(title)
                .font(.system(size: 10, weight: .semibold))
                .foregroundStyle(MenuPalette.secondary)
            Text(value)
                .font(.system(size: 16, weight: .bold))
                .foregroundStyle(MenuPalette.primary)
                .monospacedDigit()
                .lineLimit(1)
                .minimumScaleFactor(0.72)
            Text(subtitle)
                .font(.system(size: 9.5, weight: .semibold))
                .foregroundStyle(MenuPalette.secondary)
                .lineLimit(1)
                .minimumScaleFactor(0.72)
        }
        .padding(9)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(
            RoundedRectangle(cornerRadius: 8, style: .continuous)
                .fill(accent.opacity(0.12)))
        .overlay(
            RoundedRectangle(cornerRadius: 8, style: .continuous)
                .stroke(accent.opacity(0.26), lineWidth: 1))
    }
}

private struct UsageSummaryFooter: View {
    let metrics: [MetricPresentation]
    let percentMode: UsagePercentMode

    var body: some View {
        VStack(alignment: .leading, spacing: 5) {
            ForEach(metrics) { metric in
                HStack(alignment: .firstTextBaseline, spacing: 8) {
                    HStack(alignment: .firstTextBaseline, spacing: 5) {
                        Text(metric.label)
                            .font(.system(size: 10.5, weight: .semibold))
                            .foregroundStyle(MenuPalette.primary.opacity(0.90))
                        Text(metric.percentLabel(mode: percentMode))
                            .font(.system(size: 10.5, weight: .bold))
                            .foregroundStyle(MenuPalette.primary)
                    }
                    Spacer(minLength: 8)
                    if let resetText = metric.resetText {
                        Text(resetText)
                            .font(.system(size: 10, weight: .semibold))
                            .foregroundStyle(MenuPalette.secondary.opacity(0.92))
                    }
                }
                .lineLimit(1)
                .minimumScaleFactor(0.82)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }
}

private struct UsageMeterStack: View {
    let provider: ProviderPresentation
    let metrics: [MetricPresentation]
    let percentMode: UsagePercentMode

    var body: some View {
        VStack(alignment: .leading, spacing: 7) {
            ForEach(metrics) { metric in
                UsageMeterRow(
                    metric: metric,
                    accent: MenuPalette.accent(for: provider.id),
                    percentMode: percentMode)
            }
        }
    }
}

private struct UsageMeterRow: View {
    let metric: MetricPresentation
    let accent: Color
    let percentMode: UsagePercentMode

    var body: some View {
        VStack(alignment: .leading, spacing: 3) {
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                Text(metric.label)
                    .font(.system(size: 10.5, weight: .semibold))
                    .foregroundStyle(MenuPalette.primary.opacity(0.92))
                Spacer(minLength: 8)
                Text(metric.percentLabel(mode: percentMode))
                    .font(.system(size: 10.5, weight: .bold))
                    .foregroundStyle(MenuPalette.primary)
                    .monospacedDigit()
                if let pace = metric.paceText() {
                    Text(pace)
                        .font(.system(size: 9.5, weight: .semibold))
                        .foregroundStyle(MenuPalette.secondary)
                }
            }
            GeometryReader { proxy in
                let width = max(1, proxy.size.width * CGFloat(metric.displayPercent(mode: percentMode) / 100))
                ZStack(alignment: .leading) {
                    Capsule()
                        .fill(MenuPalette.primary.opacity(0.10))
                    Capsule()
                        .fill(
                            LinearGradient(
                                colors: [accent.opacity(0.92), accent.opacity(0.46)],
                                startPoint: .leading,
                                endPoint: .trailing))
                        .frame(width: width)
                }
            }
            .frame(height: 6)
        }
    }
}

private struct MetricPair: View {
    let label: String
    let value: String

    var body: some View {
        VStack(alignment: .leading, spacing: 1) {
            Text(label)
                .font(.system(size: 10, weight: .semibold))
                .foregroundStyle(MenuPalette.secondary)
            Text(value)
                .font(.system(size: 14, weight: .bold))
                .foregroundStyle(MenuPalette.primary)
                .monospacedDigit()
                .lineLimit(1)
                .minimumScaleFactor(0.72)
        }
    }
}

private struct MiniBarChart: View {
    let seed: String
    let metrics: [MetricPresentation]
    let percentMode: UsagePercentMode

    var body: some View {
        HStack(alignment: .bottom, spacing: 3) {
            ForEach(Array(values.enumerated()), id: \.offset) { _, value in
                RoundedRectangle(cornerRadius: 2, style: .continuous)
                    .fill(
                        LinearGradient(
                            colors: [MenuPalette.primary.opacity(0.88), MenuPalette.secondary.opacity(0.58)],
                            startPoint: .top,
                            endPoint: .bottom))
                .frame(width: 5, height: max(3, value * 40))
            }
        }
        .frame(maxWidth: .infinity, alignment: .bottom)
        .overlay(alignment: .bottom) {
            Rectangle()
                .fill(MenuPalette.separator)
                .frame(height: 2)
                .offset(y: 1)
        }
    }

    private var values: [CGFloat] {
        let base = metrics.map { CGFloat($0.displayPercent(mode: percentMode) / 100) }
        let source = base.isEmpty ? [0.16, 0.25, 0.34] : base
        return (0..<30).map { index in
            let metric = source[index % source.count]
            let wave = CGFloat((sin(Double(index) * 0.58) + 1) / 2)
            let bump = CGFloat((cos(Double(index) * 0.21 + Double(seed.count)) + 1) / 2)
            return min(max(0.04, metric * 0.55 + wave * 0.28 + bump * 0.20), 1)
        }
    }
}

private struct LoadingPane: View {
    var body: some View {
        VStack(spacing: 10) {
            ProgressView()
                .controlSize(.small)
            Text("Resolving usage")
                .font(.system(size: 12, weight: .medium))
                .foregroundStyle(MenuPalette.secondary)
        }
        .frame(maxWidth: .infinity, minHeight: 260)
    }
}

private struct FailurePane: View {
    let message: String

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Label("Scriba unavailable", systemImage: "exclamationmark.triangle")
                .font(.system(size: 14, weight: .semibold))
            Text(message)
                .font(.system(size: 12))
                .foregroundStyle(MenuPalette.secondary)
                .fixedSize(horizontal: false, vertical: true)
        }
        .foregroundStyle(MenuPalette.primary)
        .padding(14)
        .frame(maxWidth: .infinity, minHeight: 260, alignment: .topLeading)
    }
}
