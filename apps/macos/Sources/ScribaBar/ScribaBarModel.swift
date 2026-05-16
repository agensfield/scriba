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
    @Published var telegramSettings = TelegramSettingsState()
    @Published var telegramBotTokenInput = ""
    @Published var isSavingTelegram = false
    @Published var isRunningTelegram = false
    @Published var telegramResult: DoctorResult?
    @Published var usagePercentMode: UsagePercentMode {
        didSet {
            userDefaults.set(usagePercentMode.rawValue, forKey: ScribaBarDefaults.usagePercentMode)
            onSnapshotChanged?(snapshot)
            restartPeriodicRefresh()
        }
    }
    @Published var menuBarTextMode: MenuBarTextMode {
        didSet {
            userDefaults.set(menuBarTextMode.rawValue, forKey: ScribaBarDefaults.menuBarTextMode)
            onSnapshotChanged?(snapshot)
        }
    }
    @Published var refreshCadence: RefreshCadence {
        didSet {
            userDefaults.set(refreshCadence.rawValue, forKey: ScribaBarDefaults.refreshCadence)
            restartPeriodicRefresh()
        }
    }
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
    private let userDefaults: UserDefaults

    init(usageHistoryStore: UsageHistoryStore = UsageHistoryStore(), userDefaults: UserDefaults = .standard) {
        self.usageHistoryStore = usageHistoryStore
        self.userDefaults = userDefaults
        usagePercentMode = Self.storedValue(
            key: ScribaBarDefaults.usagePercentMode,
            defaultValue: .used,
            userDefaults: userDefaults)
        menuBarTextMode = Self.storedValue(
            key: ScribaBarDefaults.menuBarTextMode,
            defaultValue: .iconOnly,
            userDefaults: userDefaults)
        refreshCadence = Self.storedValue(
            key: ScribaBarDefaults.refreshCadence,
            defaultValue: .tenMinutes,
            userDefaults: userDefaults)
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
        await loadTelegramSettings(client: client)

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

    func loadTelegramSettings() async {
        guard let client else { return }
        await loadTelegramSettings(client: client)
    }

    func saveTelegramSettings() async {
        guard let client else {
            telegramResult = DoctorResult(title: "Telegram Config", message: "CLI is not resolved yet.")
            return
        }
        isSavingTelegram = true
        defer { isSavingTelegram = false }

        do {
            try await persistTelegramSettings(client: client)
            telegramResult = DoctorResult(title: "Telegram Config", message: "Saved \(telegramSettings.path).")
        } catch {
            telegramResult = DoctorResult(title: "Telegram Config Failed", message: error.localizedDescription)
        }
    }

    private func persistTelegramSettings(client: ScribaCLIClient) async throws {
        var args = [
            "config", "telegram", "--json",
            telegramSettings.enabled ? "--enable" : "--disable",
            "--bot-token-env", telegramSettings.botTokenEnv,
            "--chat-id", telegramSettings.chatId,
            "--session-percent", String(Int(telegramSettings.sessionPercent.rounded())),
            "--weekly-percent", String(Int(telegramSettings.weeklyPercent.rounded())),
            telegramSettings.includeErrors ? "--include-errors" : "--no-include-errors",
        ]
        if !telegramBotTokenInput.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            args.append(contentsOf: ["--bot-token", telegramBotTokenInput])
        }

        let output = try await client.text(arguments: args)
        telegramSettings = try Self.decodeTelegramSettings(output)
        telegramBotTokenInput = ""
    }

    func runTelegramAlerts(send: Bool) async {
        guard let client else {
            telegramResult = DoctorResult(title: "Telegram Alerts", message: "CLI is not resolved yet.")
            return
        }
        isRunningTelegram = true
        defer { isRunningTelegram = false }

        var args = ["telegram", "alerts", "--json"]
        if send {
            args.append("--send")
        }
        do {
            try await persistTelegramSettings(client: client)
            let output = try await client.text(arguments: args)
            telegramResult = DoctorResult(
                title: send ? "Telegram Send" : "Telegram Alerts",
                message: output.trimmingCharacters(in: .whitespacesAndNewlines))
        } catch {
            telegramResult = DoctorResult(title: "Telegram Failed", message: error.localizedDescription)
        }
    }

    func sendTelegramReset(event: WeeklyLimitResetEvent) async {
        guard let client else { return }
        let args = [
            "telegram", "reset", "--json", "--send",
            "--provider", event.providerID,
            "--label", event.label,
            "--message", event.telegramMessage,
        ]
        do {
            _ = try await client.text(arguments: args)
        } catch {
            telegramResult = DoctorResult(title: "Telegram Reset Failed", message: error.localizedDescription)
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

    private func loadTelegramSettings(client: ScribaCLIClient) async {
        do {
            let output = try await client.text(arguments: ["config", "telegram", "--json"])
            telegramSettings = try Self.decodeTelegramSettings(output)
        } catch {
            telegramResult = DoctorResult(
                title: "Telegram Config Unavailable",
                message: "The resolved Scriba CLI does not support menubar Telegram settings yet. Rebuild or relaunch ScribaBar so it picks up the bundled helper.")
        }
    }

    private func apply(snapshot: StatusSnapshot) async {
        self.snapshot = snapshot
        if selectedProviderID != "overview",
           !snapshot.providers.contains(where: { $0.providerId == selectedProviderID })
        {
            selectedProviderID = "overview"
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
        refreshTask?.cancel()
        refreshTask = Task { [weak self] in
            guard let self else { return }
            while !Task.isCancelled {
                try? await Task.sleep(nanoseconds: self.refreshCadence.nanoseconds)
                guard !Task.isCancelled else { return }
                await self.refresh()
            }
        }
    }

    private func restartPeriodicRefresh() {
        guard client != nil else { return }
        startPeriodicRefresh()
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

    private static func decodeTelegramSettings(_ output: String) throws -> TelegramSettingsState {
        guard let data = output.data(using: .utf8) else {
            throw ScribaCLIError.invalidOutput
        }
        return try JSONDecoder().decode(TelegramSettingsState.self, from: data)
    }

    private static func storedValue<T: RawRepresentable>(
        key: String,
        defaultValue: T,
        userDefaults: UserDefaults)
        -> T where T.RawValue == String
    {
        guard let raw = userDefaults.string(forKey: key),
              let value = T(rawValue: raw)
        else {
            return defaultValue
        }
        return value
    }
}

struct DoctorResult: Identifiable {
    let id = UUID()
    let title: String
    let message: String
}
