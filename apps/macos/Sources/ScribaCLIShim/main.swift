import Darwin
import Foundation

let executableURL = URL(fileURLWithPath: CommandLine.arguments[0])
let helpersURL = executableURL.deletingLastPathComponent()
let resourcesURL = helpersURL
    .deletingLastPathComponent()
    .appendingPathComponent("Resources/ScribaCLI", isDirectory: true)
let cliURL = resourcesURL.appendingPathComponent("scriba-cli.js")
let bundledBunURL = helpersURL.appendingPathComponent("bun")

func candidateRuntimeURLs() -> [URL] {
    var urls: [URL] = []
    let environment = ProcessInfo.processInfo.environment
    if FileManager.default.isExecutableFile(atPath: bundledBunURL.path) {
        urls.append(bundledBunURL)
    }

    let pathValue = [
        environment["PATH"],
        "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin",
        environment["HOME"].map { "\($0)/.bun/bin" },
    ].compactMap(\.self).joined(separator: ":")

    for dir in pathValue.split(separator: ":").map(String.init) {
        let nodeURL = URL(fileURLWithPath: dir).appendingPathComponent("node")
        if FileManager.default.isExecutableFile(atPath: nodeURL.path) {
            urls.append(nodeURL)
        }
        let bunURL = URL(fileURLWithPath: dir).appendingPathComponent("bun")
        if FileManager.default.isExecutableFile(atPath: bunURL.path) {
            urls.append(bunURL)
        }
    }

    if let home = environment["HOME"], !home.isEmpty {
        urls.append(contentsOf: glob("\(home)/.nvm/versions/node/*/bin/node"))
        urls.append(contentsOf: glob("\(home)/.fnm/node-versions/*/installation/bin/node"))
    }

    var seen = Set<String>()
    return urls.filter { seen.insert($0.standardizedFileURL.path).inserted }
}

func glob(_ pattern: String) -> [URL] {
    var globResult = glob_t()
    defer { globfree(&globResult) }
    guard Foundation.glob(pattern, 0, nil, &globResult) == 0,
          let paths = globResult.gl_pathv
    else {
        return []
    }

    return (0..<Int(globResult.gl_matchc)).compactMap { index in
        guard let path = paths[index] else { return nil }
        let url = URL(fileURLWithPath: String(cString: path))
        return FileManager.default.isExecutableFile(atPath: url.path) ? url : nil
    }
}

func run(runtimeURL: URL, arguments: [String]) -> Int32 {
    let process = Process()
    process.executableURL = runtimeURL
    process.arguments = [cliURL.path] + arguments
    process.currentDirectoryURL = resourcesURL
    process.standardInput = FileHandle.standardInput
    process.standardOutput = FileHandle.standardOutput
    process.standardError = FileHandle.standardError
    do {
        try process.run()
        process.waitUntilExit()
        return process.terminationStatus
    } catch {
        return 127
    }
}

guard FileManager.default.fileExists(atPath: cliURL.path) else {
    FileHandle.standardError.write(Data("Scriba CLI resource is missing.\n".utf8))
    exit(127)
}

let arguments = Array(CommandLine.arguments.dropFirst())
for runtimeURL in candidateRuntimeURLs() {
    let status = run(runtimeURL: runtimeURL, arguments: arguments)
    if status != 127 {
        exit(status)
    }
}

FileHandle.standardError.write(Data("ScribaBar bundled CLI needs node or bun on this Mac.\n".utf8))
exit(127)
