import Foundation
import Testing
@testable import ScribaBar

@Suite("CLI resolver")
struct CLIResolverTests {
    @Test("semantic versions compare prereleases below stable")
    func semanticVersionComparison() throws {
        let alpha = try #require(SemanticVersion("0.1.0-alpha.1"))
        let stable = try #require(SemanticVersion("0.1.0"))
        let newer = try #require(SemanticVersion("0.2.0"))

        #expect(alpha < stable)
        #expect(stable < newer)
        #expect(SemanticVersion("0.1.0-alpha.2")! > alpha)
    }

    @Test("invalid version text is rejected")
    func invalidVersion() {
        #expect(SemanticVersion("scriba dev") == nil)
        #expect(SemanticVersion("1.2") == nil)
    }

    @Test("uses system CLI when it is at least as new as bundled")
    func prefersNewEnoughSystemCLI() async throws {
        let fixture = try ResolverFixture()
        defer { fixture.cleanup() }
        try fixture.writeBundled(version: "1.2.0")
        let system = try fixture.writeSystem(version: "1.3.0")

        let resolved = await fixture.resolver().resolve()

        #expect(resolved?.url.path == system.path)
        #expect(resolved?.source == .system)
    }

    @Test("uses bundled CLI when system CLI is older")
    func rejectsOlderSystemCLI() async throws {
        let fixture = try ResolverFixture()
        defer { fixture.cleanup() }
        let bundled = try fixture.writeBundled(version: "1.3.0")
        _ = try fixture.writeSystem(version: "1.2.0")

        let resolved = await fixture.resolver().resolve()

        #expect(resolved?.url.path == bundled.path)
        #expect(resolved?.source == .bundled)
    }

    @Test("uses cached system CLI without rescanning path when cache is still valid")
    func usesCachedSystemCLI() async throws {
        let fixture = try ResolverFixture()
        defer { fixture.cleanup() }
        try fixture.writeBundled(version: "1.0.0")
        let cached = try fixture.writeExecutable(
            at: fixture.root.appendingPathComponent("cached/scriba"),
            version: "1.4.0")
        fixture.defaults.set(cached.path, forKey: "ScribaBar.ValidatedCLIPath")
        fixture.defaults.set("1.4.0", forKey: "ScribaBar.ValidatedCLIVersion")

        let resolved = await fixture.resolver(path: "/missing").resolve()

        #expect(resolved?.url.path == cached.path)
        #expect(resolved?.source == .cachedSystem)
    }
}

private struct ResolverFixture {
    let root: URL
    let appBundleURL: URL
    let systemBinURL: URL
    let defaultsSuiteName: String
    let defaults: UserDefaults

    init() throws {
        root = FileManager.default.temporaryDirectory
            .appendingPathComponent("ScribaBarResolverTests-\(UUID().uuidString)", isDirectory: true)
        appBundleURL = root.appendingPathComponent("Fake.app", isDirectory: true)
        systemBinURL = root.appendingPathComponent("bin", isDirectory: true)
        try FileManager.default.createDirectory(
            at: appBundleURL.appendingPathComponent("Contents/Helpers", isDirectory: true),
            withIntermediateDirectories: true)
        try FileManager.default.createDirectory(at: systemBinURL, withIntermediateDirectories: true)
        try """
        <?xml version="1.0" encoding="UTF-8"?>
        <!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
        <plist version="1.0"><dict><key>CFBundleIdentifier</key><string>test.fake</string></dict></plist>
        """.write(
            to: appBundleURL.appendingPathComponent("Contents/Info.plist"),
            atomically: true,
            encoding: .utf8)
        defaultsSuiteName = "ScribaBarResolverTests.\(UUID().uuidString)"
        defaults = UserDefaults(suiteName: defaultsSuiteName)!
    }

    func cleanup() {
        try? FileManager.default.removeItem(at: root)
        defaults.removePersistentDomain(forName: defaultsSuiteName)
    }

    func resolver(path: String? = nil) -> CLIResolver {
        CLIResolver(
            environment: ["PATH": path ?? systemBinURL.path],
            userDefaults: defaults,
            bundle: Bundle(url: appBundleURL)!)
    }

    @discardableResult
    func writeBundled(version: String) throws -> URL {
        try writeExecutable(
            at: appBundleURL.appendingPathComponent("Contents/Helpers/scriba"),
            version: version)
    }

    @discardableResult
    func writeSystem(version: String) throws -> URL {
        try writeExecutable(at: systemBinURL.appendingPathComponent("scriba"), version: version)
    }

    @discardableResult
    func writeExecutable(at url: URL, version: String) throws -> URL {
        try FileManager.default.createDirectory(
            at: url.deletingLastPathComponent(),
            withIntermediateDirectories: true)
        try """
        #!/bin/sh
        printf '%s\\n' '\(version)'
        """.write(to: url, atomically: true, encoding: .utf8)
        try FileManager.default.setAttributes([.posixPermissions: 0o755], ofItemAtPath: url.path)
        return url
    }
}
