import Foundation

/// Launch-at-login through a per-user LaunchAgent, which works for an
/// ad-hoc signed app outside the App Store.
enum LoginItem {
    static let label = "io.github.medpar.dial"
    static var plistURL: URL {
        URL(fileURLWithPath: NSHomeDirectory()).appendingPathComponent("Library/LaunchAgents/\(label).plist")
    }

    static var enabled: Bool { FileManager.default.fileExists(atPath: plistURL.path) }

    static func set(_ on: Bool) {
        if on {
            let app = Bundle.main.bundleURL.path
            let plist: [String: Any] = [
                "Label": label,
                "ProgramArguments": ["/usr/bin/open", "-a", app],
                "RunAtLoad": true,
                "ProcessType": "Interactive",
            ]
            try? FileManager.default.createDirectory(at: plistURL.deletingLastPathComponent(), withIntermediateDirectories: true)
            if let data = try? PropertyListSerialization.data(fromPropertyList: plist, format: .xml, options: 0) {
                try? data.write(to: plistURL)
            }
        } else {
            try? FileManager.default.removeItem(at: plistURL)
        }
    }
}
