import Foundation

struct TelegramSettingsState: Codable, Equatable {
    var path: String = ""
    var enabled: Bool = false
    var hasBotToken: Bool = false
    var botTokenEnv: String = "SCRIBA_TELEGRAM_BOT_TOKEN"
    var chatId: String = ""
    var sessionPercent: Double = 80
    var weeklyPercent: Double = 80
    var includeErrors: Bool = true

    enum CodingKeys: String, CodingKey {
        case path
        case enabled
        case hasBotToken
        case botTokenEnv
        case chatId
        case sessionPercent
        case weeklyPercent
        case includeErrors
    }
}
