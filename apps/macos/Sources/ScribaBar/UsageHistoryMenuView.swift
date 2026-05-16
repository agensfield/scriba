import SwiftUI

struct UsageHistoryMenuView: View {
    private enum Layout {
        static let height: CGFloat = 208
        static let headerHeight: CGFloat = 34
        static let pickerHeight: CGFloat = 28
        static let chartHeight: CGFloat = 92
        static let detailHeight: CGFloat = 16
        static let chartSlots = 30
    }

    @ObservedObject var model: ScribaBarModel

    private let width: CGFloat
    @State private var selectedMetricLabel: String?
    @State private var selectedEntryID: String?

    init(model: ScribaBarModel, width: CGFloat) {
        self.model = model
        self.width = width
    }

    private var selectedProvider: ProviderPresentation? {
        let presentations = model.snapshot?.presentations ?? []
        return presentations.first { $0.id == model.selectedProviderID }
            ?? presentations.first { $0.id == "codex" }
            ?? presentations.first
    }

    private var entries: [UsageHistoryEntry] {
        guard let providerID = selectedProvider?.id else { return [] }
        return model.usageHistory(for: providerID)
    }

    private var metricLabels: [String] {
        var labels: [String] = []
        for entry in entries {
            for metric in entry.metrics where !labels.contains(metric.label) {
                labels.append(metric.label)
            }
        }
        return labels
    }

    private var effectiveMetricLabel: String? {
        if let selectedMetricLabel, metricLabels.contains(selectedMetricLabel) {
            return selectedMetricLabel
        }
        return metricLabels.first
    }

    private var chartPoints: [UsageHistoryChartPoint] {
        guard let label = effectiveMetricLabel else { return [] }
        return entries.suffix(30).compactMap { entry in
            guard let metric = entry.metrics.first(where: { $0.label == label }) else { return nil }
            return UsageHistoryChartPoint(entry: entry, metric: metric)
        }
    }

    private var selectedPoint: UsageHistoryChartPoint? {
        chartPoints.first { $0.id == selectedEntryID } ?? chartPoints.last
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            header

            if metricLabels.count > 1 {
                Picker(selection: Binding(
                    get: { effectiveMetricLabel ?? "" },
                    set: { label in
                        selectedMetricLabel = label
                        selectedEntryID = nil
                    })) {
                        ForEach(metricLabels, id: \.self) { label in
                            Text(shortMetricTitle(label)).tag(label)
                        }
                    } label: {
                        EmptyView()
                    }
                    .labelsHidden()
                    .pickerStyle(.segmented)
                    .frame(height: Layout.pickerHeight)
            } else {
                Color.clear.frame(height: Layout.pickerHeight)
            }

            if chartPoints.isEmpty {
                emptyState
            } else {
                HoverUsageHistoryChart(
                    points: chartPoints,
                    slotCount: Layout.chartSlots,
                    selectedID: $selectedEntryID)
                    .frame(height: Layout.chartHeight)

                detailLine
            }
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 12)
        .frame(width: width, height: Layout.height, alignment: .topLeading)
        .transaction { transaction in
            transaction.animation = nil
        }
    }

    private var header: some View {
        HStack(alignment: .firstTextBaseline, spacing: 8) {
            VStack(alignment: .leading, spacing: 2) {
                Text("Usage History")
                    .font(.system(size: 13, weight: .semibold))
                    .foregroundStyle(MenuPalette.primary)
                Text(selectedProvider?.displayName ?? "Scriba")
                    .font(.system(size: 10, weight: .semibold))
                    .foregroundStyle(MenuPalette.secondary)
            }
            Spacer()
            Text("\(entries.count) samples")
                .font(.system(size: 10, weight: .semibold))
                .foregroundStyle(MenuPalette.secondary)
        }
        .frame(height: Layout.headerHeight, alignment: .top)
    }

    private var emptyState: some View {
        Text("Collecting usage history")
            .font(.system(size: 12, weight: .medium))
            .foregroundStyle(MenuPalette.secondary)
            .frame(maxWidth: .infinity)
            .frame(height: Layout.chartHeight + Layout.detailHeight + 10)
    }

    private var detailLine: some View {
        let point = selectedPoint
        return Text(point.map(detailText(for:)) ?? "No selected sample")
            .font(.system(size: 10, weight: .semibold))
            .foregroundStyle(MenuPalette.secondary)
            .lineLimit(1)
            .truncationMode(.tail)
            .frame(height: Layout.detailHeight, alignment: .leading)
    }

    private func detailText(for point: UsageHistoryChartPoint) -> String {
        let time = point.entry.recordedAt.formatted(date: .omitted, time: .shortened)
        var pieces = [
            "\(time): \(Int(point.metric.usedPercent.rounded()))% used",
        ]
        if let resetAt = point.metric.resetAt {
            pieces.append("resets \(resetAt.formatted(.relative(presentation: .named)))")
        }
        return pieces.joined(separator: " · ")
    }

    private func shortMetricTitle(_ label: String) -> String {
        label
            .replacingOccurrences(of: " limit", with: "", options: .caseInsensitive)
            .replacingOccurrences(of: "weekly", with: "Week", options: .caseInsensitive)
            .replacingOccurrences(of: "5h", with: "5h", options: .caseInsensitive)
    }
}

private struct UsageHistoryChartPoint: Identifiable, Equatable {
    var id: String { entry.id }

    let entry: UsageHistoryEntry
    let metric: UsageHistoryMetric
}

private struct HoverUsageHistoryChart: View {
    let points: [UsageHistoryChartPoint]
    let slotCount: Int
    @Binding var selectedID: String?

    private struct Slot: Identifiable {
        let index: Int
        let point: UsageHistoryChartPoint?

        var id: Int { index }
    }

    private var selectedEffectiveID: String? {
        if let selectedID, points.contains(where: { $0.id == selectedID }) {
            return selectedID
        }
        return points.last?.id
    }

    private var slots: [Slot] {
        let visiblePoints = Array(points.suffix(slotCount))
        let emptyCount = max(0, slotCount - visiblePoints.count)
        return (0..<slotCount).map { index in
            let pointIndex = index - emptyCount
            return Slot(
                index: index,
                point: pointIndex >= 0 && pointIndex < visiblePoints.count ? visiblePoints[pointIndex] : nil)
        }
    }

    var body: some View {
        VStack(spacing: 5) {
            GeometryReader { proxy in
                let maxHeight = max(proxy.size.height - 14, 1)
                HStack(alignment: .bottom, spacing: 4) {
                    ForEach(slots) { slot in
                        let point = slot.point
                        let isSelected = point?.id == selectedEffectiveID
                        RoundedRectangle(cornerRadius: 2.5, style: .continuous)
                            .fill(barFill(hasPoint: point != nil, isSelected: isSelected))
                            .frame(width: 8)
                            .frame(height: barHeight(point: point, maxHeight: maxHeight))
                            .overlay(alignment: .top) {
                                if isSelected {
                                    Circle()
                                        .fill(MenuPalette.selectedText)
                                        .frame(width: 5, height: 5)
                                        .offset(y: -7)
                                }
                            }
                            .contentShape(Rectangle())
                            .onHover { isInside in
                                guard let point else { return }
                                if isInside {
                                    selectedID = point.id
                                } else if selectedID == point.id {
                                    selectedID = nil
                                }
                            }
                    }
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .bottom)
                .overlay(alignment: .bottom) {
                    Rectangle()
                        .fill(MenuPalette.separator)
                        .frame(height: 2)
                        .offset(y: 1)
                }
            }
        }
        .padding(.top, 8)
    }

    private func barHeight(point: UsageHistoryChartPoint?, maxHeight: CGFloat) -> CGFloat {
        guard let point else { return 4 }
        return max(5, maxHeight * CGFloat(point.metric.usedPercent / 100))
    }

    private func barFill(hasPoint: Bool, isSelected: Bool) -> LinearGradient {
        guard hasPoint else {
            return LinearGradient(
                colors: [MenuPalette.primary.opacity(0.08), MenuPalette.primary.opacity(0.05)],
                startPoint: .top,
                endPoint: .bottom)
        }
        return LinearGradient(
            colors: isSelected
                ? [MenuPalette.selectedText.opacity(0.98), MenuPalette.selectedText.opacity(0.62)]
                : [MenuPalette.primary.opacity(0.78), MenuPalette.secondary.opacity(0.46)],
            startPoint: .top,
            endPoint: .bottom)
    }
}
