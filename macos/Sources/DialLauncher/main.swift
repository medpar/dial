import AppKit

// A Dock launcher for one account ("claude:work", "codex:team"). Clicking it
// brings that account's desktop window to the front, opening it if needed,
// then the launcher quits. Built by Dial.app (Dock icons…) from a template;
// the target lives in Info.plist under DialTarget.

final class Launcher: NSObject, NSApplicationDelegate {
    let target = Bundle.main.object(forInfoDictionaryKey: "DialTarget") as? String ?? ""

    var dial: String {
        let fm = FileManager.default
        for p in ["/Applications/Dial.app/Contents/Helpers/dial",
                  "\(NSHomeDirectory())/Applications/Dial.app/Contents/Helpers/dial",
                  "\(NSHomeDirectory())/.local/bin/dial"] where fm.isExecutableFile(atPath: p) {
            return p
        }
        return "/usr/local/bin/dial"
    }

    func run(_ args: [String]) -> Data {
        let p = Process()
        p.executableURL = URL(fileURLWithPath: dial)
        p.arguments = args
        var env = ProcessInfo.processInfo.environment
        env["NO_COLOR"] = "1"
        env["PATH"] = "\(NSHomeDirectory())/.local/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
        p.environment = env
        let out = Pipe()
        p.standardOutput = out
        p.standardError = FileHandle.nullDevice
        do { try p.run() } catch { return Data() }
        let data = out.fileHandleForReading.readDataToEndOfFile()
        p.waitUntilExit()
        return data
    }

    struct Where: Decodable { var running: Bool; var pid: Int32?; var kind: String }

    func locate() -> Where? {
        try? JSONDecoder().decode(Where.self, from: run(["desktop", "where", target, "--json"]))
    }

    func bringForward(_ pid: Int32) {
        guard let app = NSRunningApplication(processIdentifier: pid) else { return }
        NSApp.yieldActivation(to: app)
        app.activate(options: [.activateAllWindows])
    }

    func applicationDidFinishLaunching(_ note: Notification) {
        DispatchQueue.global(qos: .userInitiated).async { [self] in
            if let w = locate(), w.running, let pid = w.pid {
                DispatchQueue.main.sync { bringForward(pid) }
            } else {
                _ = run(["desktop", "show", target, "--json"])
                for _ in 0..<80 {
                    if let w = locate(), w.running, let pid = w.pid {
                        Thread.sleep(forTimeInterval: 0.6) // let its window appear
                        DispatchQueue.main.sync { bringForward(pid) }
                        break
                    }
                    Thread.sleep(forTimeInterval: 0.25)
                }
            }
            DispatchQueue.main.async { NSApp.terminate(nil) }
        }
    }
}

let app = NSApplication.shared
let delegate = Launcher()
app.delegate = delegate
app.run()
