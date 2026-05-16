import Foundation

struct SemanticVersion: Comparable, Equatable {
    let major: Int
    let minor: Int
    let patch: Int
    let prerelease: String?

    init?(_ raw: String) {
        let trimmed = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        let parts = trimmed.split(separator: "-", maxSplits: 1).map(String.init)
        let numbers = parts[0].split(separator: ".").map(String.init)
        guard numbers.count >= 3,
              let major = Int(numbers[0]),
              let minor = Int(numbers[1]),
              let patch = Int(numbers[2])
        else {
            return nil
        }
        self.major = major
        self.minor = minor
        self.patch = patch
        self.prerelease = parts.count > 1 ? parts[1] : nil
    }

    static func < (lhs: SemanticVersion, rhs: SemanticVersion) -> Bool {
        if lhs.major != rhs.major { return lhs.major < rhs.major }
        if lhs.minor != rhs.minor { return lhs.minor < rhs.minor }
        if lhs.patch != rhs.patch { return lhs.patch < rhs.patch }
        switch (lhs.prerelease, rhs.prerelease) {
        case (nil, nil):
            return false
        case (nil, _?):
            return false
        case (_?, nil):
            return true
        case let (left?, right?):
            return left.localizedStandardCompare(right) == .orderedAscending
        }
    }
}

struct CLIInfo: Equatable {
    let url: URL
    let version: SemanticVersion
    let source: Source

    enum Source: String {
        case cachedSystem
        case system
        case bundled
    }
}

struct CLIResolver {
    var fileManager: FileManager = .default
    var environment: [String: String] = ProcessInfo.processInfo.environment
    var userDefaults: UserDefaults = .standard
    var bundle: Bundle = .main
    var runner = ProcessRunner()

    private let cachePathKey = "ScribaBar.ValidatedCLIPath"
    private let cacheVersionKey = "ScribaBar.ValidatedCLIVersion"

    func resolve() async -> CLIInfo? {
        let bundled = bundledCLIInfo()
        if let cached = cachedSystemCLIInfo(), isSystem(cached, acceptableAgainst: bundled) {
            return cached
        }

        if let system = bestSystemCLIInfo(), isSystem(system, acceptableAgainst: bundled) {
            cache(system)
            return system
        }

        if let bundled {
            return bundled
        }

        return bestSystemCLIInfo()
    }

    private func isSystem(_ info: CLIInfo, acceptableAgainst bundled: CLIInfo?) -> Bool {
        guard info.source == .system || info.source == .cachedSystem else { return false }
        guard supportsMenuBarConfig(info.url) else { return false }
        guard let bundled else { return true }
        return info.version >= bundled.version
    }

    private func cachedSystemCLIInfo() -> CLIInfo? {
        guard let path = userDefaults.string(forKey: cachePathKey),
              let versionText = userDefaults.string(forKey: cacheVersionKey),
              let version = SemanticVersion(versionText)
        else {
            return nil
        }
        let url = URL(fileURLWithPath: path)
        guard isExecutable(url) else { return nil }
        return CLIInfo(url: url, version: version, source: .cachedSystem)
    }

    private func bestSystemCLIInfo() -> CLIInfo? {
        systemCandidates()
            .compactMap { url -> CLIInfo? in
                guard isExecutable(url), let version = version(for: url) else { return nil }
                return CLIInfo(url: url, version: version, source: .system)
            }
            .sorted { $0.version > $1.version }
            .first
    }

    private func bundledCLIInfo() -> CLIInfo? {
        for url in bundledCandidates() where isExecutable(url) {
            if let version = version(for: url) {
                return CLIInfo(url: url, version: version, source: .bundled)
            }
        }
        return nil
    }

    private func bundledCandidates() -> [URL] {
        var urls: [URL] = []
        let bundleURL = bundle.bundleURL
        urls.append(bundleURL.appendingPathComponent("Contents/Helpers/scriba"))
        if let resourceURL = bundle.resourceURL {
            urls.append(resourceURL.appendingPathComponent("scriba"))
        }
        return unique(urls)
    }

    private func systemCandidates() -> [URL] {
        var urls: [URL] = []
        if let explicit = environment["SCRIBA_CLI"]?.trimmingCharacters(in: .whitespacesAndNewlines),
           !explicit.isEmpty
        {
            urls.append(URL(fileURLWithPath: explicit))
        }

        let pathValue = environment["PATH"] ?? "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin"
        for dir in pathValue.split(separator: ":").map(String.init) {
            urls.append(URL(fileURLWithPath: dir).appendingPathComponent("scriba"))
        }

        urls.append(contentsOf: [
            URL(fileURLWithPath: "/opt/homebrew/bin/scriba"),
            URL(fileURLWithPath: "/usr/local/bin/scriba"),
            URL(fileURLWithPath: "/usr/bin/scriba"),
        ])
        return unique(urls)
    }

    private func version(for url: URL) -> SemanticVersion? {
        let result = runner.run(url: url, arguments: ["--version"], timeout: 1.5)
        guard result.exitCode == 0 else { return nil }
        return SemanticVersion(result.stdout)
    }

    private func supportsMenuBarConfig(_ url: URL) -> Bool {
        let result = runner.run(url: url, arguments: ["config", "path"], timeout: 1.5)
        return result.exitCode == 0
    }

    private func isExecutable(_ url: URL) -> Bool {
        fileManager.isExecutableFile(atPath: url.path)
    }

    private func cache(_ info: CLIInfo) {
        userDefaults.set(info.url.path, forKey: cachePathKey)
        userDefaults.set("\(info.version.major).\(info.version.minor).\(info.version.patch)\(info.version.prerelease.map { "-\($0)" } ?? "")", forKey: cacheVersionKey)
    }

    private func unique(_ urls: [URL]) -> [URL] {
        var seen = Set<String>()
        return urls.filter { seen.insert($0.path).inserted }
    }
}

struct ProcessResult {
    let exitCode: Int32
    let stdout: String
    let stderr: String
}

struct ProcessRunner {
    func run(url: URL, arguments: [String], timeout: TimeInterval) -> ProcessResult {
        let tempDirectory = FileManager.default.temporaryDirectory
            .appendingPathComponent("ScribaBar-\(UUID().uuidString)", isDirectory: true)
        let stdoutURL = tempDirectory.appendingPathComponent("stdout")
        let stderrURL = tempDirectory.appendingPathComponent("stderr")

        do {
            try FileManager.default.createDirectory(at: tempDirectory, withIntermediateDirectories: true)
            FileManager.default.createFile(atPath: stdoutURL.path, contents: nil)
            FileManager.default.createFile(atPath: stderrURL.path, contents: nil)
        } catch {
            return ProcessResult(exitCode: 1, stdout: "", stderr: "\(error)")
        }
        defer {
            try? FileManager.default.removeItem(at: tempDirectory)
        }

        guard let stdout = try? FileHandle(forWritingTo: stdoutURL),
              let stderr = try? FileHandle(forWritingTo: stderrURL)
        else {
            return ProcessResult(exitCode: 1, stdout: "", stderr: "Unable to open process output files.")
        }
        defer {
            try? stdout.close()
            try? stderr.close()
        }

        let process = Process()
        process.executableURL = url
        process.arguments = arguments
        process.standardOutput = stdout
        process.standardError = stderr

        do {
            try process.run()
        } catch {
            return ProcessResult(exitCode: 127, stdout: "", stderr: "\(error)")
        }

        let group = DispatchGroup()
        group.enter()
        DispatchQueue.global(qos: .utility).async {
            process.waitUntilExit()
            group.leave()
        }

        if group.wait(timeout: .now() + timeout) == .timedOut {
            process.terminate()
            _ = group.wait(timeout: .now() + 1)
            return ProcessResult(exitCode: 124, stdout: "", stderr: "Timed out")
        }

        try? stdout.synchronize()
        try? stderr.synchronize()

        let out = (try? String(contentsOf: stdoutURL, encoding: .utf8)) ?? ""
        let err = (try? String(contentsOf: stderrURL, encoding: .utf8)) ?? ""
        return ProcessResult(exitCode: process.terminationStatus, stdout: out, stderr: err)
    }
}
