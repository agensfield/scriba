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

    init(
        type: String,
        label: String,
        text: String?,
        value: String?,
        used: Double?,
        limit: Double?,
        resetsAt: String?)
    {
        self.type = type
        self.label = label
        self.text = text
        self.value = value
        self.used = used
        self.limit = limit
        self.resetsAt = resetsAt
    }

    enum CodingKeys: String, CodingKey {
        case type
        case label
        case text
        case value
        case used
        case limit
        case resetsAt
    }

    init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        type = try container.decode(String.self, forKey: .type)
        label = try container.decode(String.self, forKey: .label)
        text = try container.decodeIfPresent(String.self, forKey: .text)
        if let stringValue = try? container.decodeIfPresent(String.self, forKey: .value) {
            value = stringValue
        } else if let numberValue = try? container.decodeIfPresent(Double.self, forKey: .value) {
            value = numberValue.formatted(.number)
        } else {
            value = nil
        }
        used = try container.decodeIfPresent(Double.self, forKey: .used)
        limit = try container.decodeIfPresent(Double.self, forKey: .limit)
        resetsAt = try container.decodeIfPresent(String.self, forKey: .resetsAt)
    }
}
