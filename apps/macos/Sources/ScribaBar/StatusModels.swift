import Foundation

struct StatusSnapshot: Decodable {
    let schemaVersion: String
    let generatedAt: String
    let providers: [ProviderSnapshot]
}

struct ProviderSnapshot: Decodable, Identifiable {
    var id: String { providerId }

    let providerId: String
    let displayName: String
    let state: String
    let lines: [StatusLine]
}

struct StatusLine: Decodable, Identifiable {
    var id: String { "\(type):\(label):\(text ?? value ?? ""):\(used ?? -1)" }

    let type: String
    let label: String
    let text: String?
    let value: String?
    let used: Double?
    let limit: Double?
    let resetsAt: String?
}
