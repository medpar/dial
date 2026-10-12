import Foundation

// Mirrors the JSON written by `dial status --json` and `dial use --json`.

struct DialStatus: Decodable {
    var version: String
    var host: String
    var generatedAt: Date
    var providers: [ProviderStatus]
    var desktop: DesktopInfo
    var remotes: [RemoteStatus]?
    var warnings: [String]?

    func provider(_ name: String) -> ProviderStatus? { providers.first { $0.provider == name } }
}

struct ProviderStatus: Decodable, Identifiable {
    var provider: String
    var active: String?
    var profiles: [ProfileStatus]
    var id: String { provider }
}

struct ProfileStatus: Decodable, Identifiable, Equatable {
    var id: String
    var label: String
    var email: String?
    var org: String?
    var plan: String?
    var active: Bool
    var loggedIn: Bool
    var desktop: String?
    var window: Bool?
    var usage: UsageReport?

    static func == (a: ProfileStatus, b: ProfileStatus) -> Bool {
        a.id == b.id && a.active == b.active && a.usage?.fetchedAt == b.usage?.fetchedAt && a.desktop == b.desktop
    }

    /// Highest used percentage across the account's windows.
    var maxUsed: Double? {
        guard let w = usage?.windows, !w.isEmpty else { return nil }
        return w.map(\.usedPercent).max()
    }
}

struct UsageReport: Decodable {
    var provider: String?
    var profileId: String?
    var plan: String?
    var windows: [UsageWindow]?
    var extra: [UsageExtra]?
    var limitReached: Bool?
    var fetchedAt: Date
    var error: String?
    var stale: Bool?
}

struct UsageWindow: Decodable, Identifiable {
    var key: String
    var label: String
    var usedPercent: Double
    var resetsAt: Date?
    var windowSeconds: Int?
    var id: String { key }
}

struct UsageExtra: Decodable, Identifiable {
    var label: String
    var value: String
    var id: String { label }
}

struct DesktopInfo: Decodable {
    var claude: DesktopStatus
    var codex: DesktopStatus
}

struct DesktopStatus: Decodable {
    var installed: Bool
    var running: Bool
    var accounts: [DesktopAccount]?
    var windows: [String]?
}

struct DesktopAccount: Decodable {
    var accountUuid: String
    var profileId: String?
    var label: String
    var live: Bool
    var window: Bool
    var saved: Bool
}

struct RemoteStatus: Decodable, Identifiable {
    var name: String
    var ssh: String
    var auto: Bool
    var sync: [String: SyncResult]?
    var id: String { name }
}

struct SyncResult: Decodable {
    var remote: String
    var at: Date
    var ok: Bool
    var message: String?
}

struct UseResult: Decodable {
    var ok: Bool
    var provider: String
    var profile: String
    var label: String
    var cli: String
    var desktop: String
    var remotes: [SyncResult]?
    var warnings: [String]?
}

struct SimpleResult: Decodable {
    var ok: Bool
    var message: String?
}

extension JSONDecoder {
    static let dial: JSONDecoder = {
        let d = JSONDecoder()
        d.keyDecodingStrategy = .convertFromSnakeCase
        let frac = ISO8601DateFormatter()
        frac.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        let plain = ISO8601DateFormatter()
        plain.formatOptions = [.withInternetDateTime]
        d.dateDecodingStrategy = .custom { dec in
            let s = try dec.singleValueContainer().decode(String.self)
            // Go writes up to nanoseconds; ISO8601DateFormatter takes milliseconds.
            var t = s
            if let dot = s.firstIndex(of: "."), let end = s[dot...].firstIndex(where: { $0 == "Z" || $0 == "+" || $0 == "-" }) {
                let digits = s[s.index(after: dot)..<end]
                if digits.count > 3 { t = String(s[..<s.index(dot, offsetBy: 4)]) + String(s[end...]) }
            }
            if let date = frac.date(from: t) ?? plain.date(from: t) { return date }
            if s.hasPrefix("0001-01-01") { return .distantPast }
            throw DecodingError.dataCorrupted(.init(codingPath: dec.codingPath, debugDescription: "bad date \(s)"))
        }
        return d
    }()
}
