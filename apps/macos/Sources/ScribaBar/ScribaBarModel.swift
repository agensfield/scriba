import Foundation
import SwiftUI

@MainActor
final class ScribaBarModel: ObservableObject {
    @Published var snapshot: StatusSnapshot?
    @Published var state: LoadState = .loading
    @Published var cliDescription = "Resolving scriba..."
    @Published var lastUpdated: Date?
    @Published var isRefreshing = false
    @Published var isRunningDoctor = false
    @Published var doctorResult: DoctorResult?
    @Published var selectedProviderID = "overview"
    @Published var notificationDescription = "Checking..."
    @Published private(set) var usageHistoryByProvider: [String: [UsageHistoryEntry]] = [:]

    var onSnapshotChanged: ((StatusSnapshot?) -> Void)?
    var onWeeklyLimitResetEarly: ((WeeklyLimitResetEvent) -> Void)?

    enum LoadState: Equatable {
        case loading
        case ready
        case failed(String)
    }

    private var client: ScribaCLIClient?
    private var refreshTask: Task<Void, Never>?
    private let usageHistoryStore: UsageHistoryStore
    private let weeklyResetMonitor = WeeklyResetMonitor()
    private let refreshIntervalNanoseconds: UInt64 = 10 * 60 * 1_000_000_000

    init(usageHistoryStore: UsageHistoryStore = UsageHistoryStore()) {
        self.usageHistoryStore = usageHistoryStore
        usageHistoryByProvider = usageHistoryStore.load()
    }

    deinit {
        refreshTask?.cancel()
    }

    func start() async {
        refreshTask?.cancel()
        let resolver = CLIResolver()
        guard let info = await resolver.resolve() else {
            state = .failed("No usable scriba CLI found.")
            cliDescription = "CLI unavailable"
            return
        }

        cliDescription = Self.describeCLI(info)
        let client = ScribaCLIClient(cliURL: info.url)
        self.client = client

        await loadFastSnapshot(client: client)
        Task {
            await self.refresh()
        }
        startPeriodicRefresh()
    }

    func refresh() async {
        guard let client else { return }
        guard !isRefreshing else { return }
        isRefreshing = true
        defer { isRefreshing = false }

        do {
            let snapshot = try await client.status(arguments: ["status", "--json"])
            await apply(snapshot: snapshot)
        } catch {
            if snapshot == nil {
                state = .failed(error.localizedDescription)
            }
        }
    }

    func runDoctor() async {
        guard let client else {
            doctorResult = DoctorResult(title: "Scriba Doctor", message: "CLI is not resolved yet.")
            return
        }

        isRunningDoctor = true
        defer { isRunningDoctor = false }

        do {
            let output = try await client.text(arguments: ["doctor", "--no-remote"])
            doctorResult = DoctorResult(
                title: "Scriba Doctor",
                message: output.trimmingCharacters(in: .whitespacesAndNewlines))
        } catch {
            doctorResult = DoctorResult(title: "Scriba Doctor Failed", message: error.localizedDescription)
        }
    }

    func usageHistory(for providerID: String) -> [UsageHistoryEntry] {
        usageHistoryByProvider[providerID] ?? []
    }

    private func loadFastSnapshot(client: ScribaCLIClient) async {
        do {
            let snapshot = try await client.status(arguments: ["status", "--fast", "--json"])
            await apply(snapshot: snapshot)
        } catch {
            state = .failed(error.localizedDescription)
        }
    }

    private func apply(snapshot: StatusSnapshot) async {
        self.snapshot = snapshot
        if selectedProviderID == "overview",
           snapshot.providers.contains(where: { $0.providerId == "codex" })
        {
            selectedProviderID = "codex"
        }
        lastUpdated = Date()
        state = .ready
        usageHistoryByProvider = usageHistoryStore.observe(snapshot: snapshot, now: lastUpdated ?? Date())
        onSnapshotChanged?(snapshot)
        for event in weeklyResetMonitor.observe(snapshot: snapshot) {
            onWeeklyLimitResetEarly?(event)
        }
    }

    private func startPeriodicRefresh() {
        refreshTask = Task { [weak self] in
            guard let self else { return }
            while !Task.isCancelled {
                try? await Task.sleep(nanoseconds: self.refreshIntervalNanoseconds)
                guard !Task.isCancelled else { return }
                await self.refresh()
            }
        }
    }

    private static func describeCLI(_ info: CLIInfo) -> String {
        let source = switch info.source {
        case .cachedSystem:
            "system"
        case .system:
            "system"
        case .bundled:
            "bundled"
        }
        return "\(source) \(info.version.major).\(info.version.minor).\(info.version.patch)\(info.version.prerelease.map { "-\($0)" } ?? "")"
    }
}

struct DoctorResult: Identifiable {
    let id = UUID()
    let title: String
    let message: String
}
