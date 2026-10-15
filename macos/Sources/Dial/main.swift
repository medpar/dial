import AppKit

let app = NSApplication.shared
let args = CommandLine.arguments

// Helper modes used by the dial CLI; they never start the menu bar UI.
if let i = args.firstIndex(of: "--activate-pid"), i + 1 < args.count, let pid = Int32(args[i + 1]) {
    exit(Activator.activate(pid: pid) ? 0 : 1)
}
if args.contains("--restore-dock") {
    Launchers.restoreDock()
    print("Dock restored: original Claude and Codex icons are back")
    exit(0)
}
if args.contains("--make-launchers") {
    let made = Launchers.makeAll(pin: args.contains("--pin") || args.contains("--replace"), replace: args.contains("--replace"))
    print(made.isEmpty ? "No desktop accounts to make Dock icons for." : "Dock icons are in \(Launchers.folder.path)")
    exit(0)
}

app.setActivationPolicy(.accessory)
let delegate = MainActor.assumeIsolated { AppDelegate() }
app.delegate = delegate
app.run()
