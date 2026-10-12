import Foundation

/// Runs the bundled `dial` binary. All account logic lives in the CLI; the
/// app only renders its JSON, so the menu bar and terminal never disagree.
enum DialClient {
    static let binary: String = {
        let fm = FileManager.default
        let bundled = Bundle.main.bundleURL.appendingPathComponent("Contents/Helpers/dial").path
        if fm.isExecutableFile(atPath: bundled) { return bundled }
        for p in ["\(NSHomeDirectory())/.local/bin/dial", "/opt/homebrew/bin/dial", "/usr/local/bin/dial"] where fm.isExecutableFile(atPath: p) {
            return p
        }
        return "dial"
    }()

    struct Failure: Error, LocalizedError {
        var message: String
        var errorDescription: String? { message }
    }

    /// Runs dial with args off the main thread and returns stdout.
    static func run(_ args: [String], timeout: TimeInterval = 90) async throws -> Data {
        try await withCheckedThrowingContinuation { cont in
            DispatchQueue.global(qos: .userInitiated).async {
                let p = Process()
                p.executableURL = URL(fileURLWithPath: binary)
                p.arguments = args
                var env = ProcessInfo.processInfo.environment
                env["NO_COLOR"] = "1"
                let extra = ["\(NSHomeDirectory())/.local/bin", "/opt/homebrew/bin", "/usr/local/bin"]
                env["PATH"] = (extra + [env["PATH"] ?? "/usr/bin:/bin:/usr/sbin:/sbin"]).joined(separator: ":")
                p.environment = env
                let out = Pipe(), err = Pipe()
                p.standardOutput = out
                p.standardError = err
                p.standardInput = FileHandle.nullDevice
                do { try p.run() } catch {
                    cont.resume(throwing: Failure(message: "cannot run dial: \(error.localizedDescription)"))
                    return
                }
                let timer = DispatchWorkItem { if p.isRunning { p.terminate() } }
                DispatchQueue.global().asyncAfter(deadline: .now() + timeout, execute: timer)
                // Read before waiting so a large reply never blocks the pipe.
                let data = out.fileHandleForReading.readDataToEndOfFile()
                let errData = err.fileHandleForReading.readDataToEndOfFile()
                p.waitUntilExit()
                timer.cancel()
                if p.terminationStatus != 0 && data.isEmpty {
                    let msg = String(data: errData, encoding: .utf8)?
                        .replacingOccurrences(of: "error: ", with: "")
                        .trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
                    cont.resume(throwing: Failure(message: msg.isEmpty ? "dial exited with \(p.terminationStatus)" : msg))
                    return
                }
                cont.resume(returning: data)
            }
        }
    }

    static func decode<T: Decodable>(_ type: T.Type, _ args: [String], timeout: TimeInterval = 90) async throws -> T {
        let data = try await run(args, timeout: timeout)
        do {
            return try JSONDecoder.dial.decode(T.self, from: data)
        } catch {
            throw Failure(message: "unexpected reply from dial: \(error)")
        }
    }

    static func status(refresh: Bool, force: Bool = false) async throws -> DialStatus {
        var args = ["status", "--json"]
        if force { args.append("--force") } else if refresh { args.append("--refresh") }
        return try await decode(DialStatus.self, args)
    }

    static func use(_ provider: String, _ id: String, scope: String? = nil) async throws -> UseResult {
        var args = ["use", "\(provider):\(id)", "--json"]
        if let scope { args.append("--\(scope)") }
        return try await decode(UseResult.self, args, timeout: 120)
    }

    static func open(_ provider: String, _ id: String) async throws -> SimpleResult {
        try await decode(SimpleResult.self, ["open", "\(provider):\(id)", "--json"])
    }

    /// Opens Terminal running `dial <args>` (no Automation permission needed:
    /// Terminal runs a .command file). The window closes itself when the task
    /// ends: right away on success, after 30 s or a key press on failure so the
    /// error stays readable.
    static func openInTerminal(_ args: [String], title: String) {
        let dir = URL(fileURLWithPath: NSHomeDirectory()).appendingPathComponent(".dial/tmp", isDirectory: true)
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        let file = dir.appendingPathComponent("\(title.replacingOccurrences(of: "/", with: "-")).command")
        let quoted = ([binary] + args).map { "'" + $0.replacingOccurrences(of: "'", with: "'\\''") + "'" }.joined(separator: " ")
        let script = """
        #!/bin/zsh -l
        printf '\\e]0;dial\\a'
        clear
        # Ctrl-C stops the task, not this wrapper, so the window still closes.
        trap 'true' INT
        \(quoted)
        code=$?
        if [ $code -eq 0 ]; then
          print -P '\\n%F{green}✓ Done%f — closing…'
          sleep 1.2
        else
          print -P "\\n%F{yellow}Finished with errors (exit $code).%f Press any key to close (closes in 30 s)."
          read -k1 -s -t 30
        fi
        # Find this window while the shell is alive (its tty goes away on exit),
        # then close it by id once the shell has exited.
        me=$(tty)
        wid=$(osascript -e "tell application \\"Terminal\\"" \\
          -e "repeat with w in windows" \\
          -e "if tty of selected tab of w is \\"$me\\" then return id of w" \\
          -e "end repeat" -e "end tell" 2>/dev/null)
        if [ -n "$wid" ]; then
          nohup osascript -e "delay 0.5" -e "tell application \\"Terminal\\" to close (window id $wid) saving no" >/dev/null 2>&1 &!
        fi
        exit $code

        """
        try? script.write(to: file, atomically: true, encoding: .utf8)
        try? FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: file.path)
        let p = Process()
        p.executableURL = URL(fileURLWithPath: "/usr/bin/open")
        p.arguments = ["-a", "Terminal", file.path]
        try? p.run()
    }
}
