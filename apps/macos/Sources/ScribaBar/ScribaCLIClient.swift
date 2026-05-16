import Foundation

struct ScribaCLIClient {
    let cliURL: URL
    var timeout: TimeInterval = 20

    func status(arguments: [String]) async throws -> StatusSnapshot {
        let output = try await text(arguments: arguments)
        guard let data = output.data(using: .utf8) else {
            throw ScribaCLIError.invalidOutput
        }
        return try JSONDecoder().decode(StatusSnapshot.self, from: data)
    }

    func text(arguments: [String]) async throws -> String {
        try await withCheckedThrowingContinuation { continuation in
            DispatchQueue.global(qos: .utility).async {
                let result = ProcessRunner().run(url: cliURL, arguments: arguments, timeout: timeout)
                guard result.exitCode == 0 else {
                    continuation.resume(throwing: ScribaCLIError.commandFailed(result.stderr.isEmpty ? result.stdout : result.stderr))
                    return
                }
                continuation.resume(returning: result.stdout)
            }
        }
    }
}

enum ScribaCLIError: LocalizedError {
    case commandFailed(String)
    case invalidOutput

    var errorDescription: String? {
        switch self {
        case let .commandFailed(message):
            return message.trimmingCharacters(in: .whitespacesAndNewlines)
        case .invalidOutput:
            return "Scriba returned invalid text output."
        }
    }
}
