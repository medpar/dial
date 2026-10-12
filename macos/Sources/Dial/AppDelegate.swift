import AppKit
import Carbon
import SwiftUI

@MainActor
final class AppDelegate: NSObject, NSApplicationDelegate, NSPopoverDelegate {
    private var item: NSStatusItem!
    private var popover: NSPopover?
    private let store = Store()
    private var hotKey: HotKey?
    private var monitor: Any?

    func applicationDidFinishLaunching(_ note: Notification) {
        item = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
        if let b = item.button {
            b.image = StatusIcon.image(claude: nil, codex: nil)
            b.imagePosition = .imageLeading
            b.target = self
            b.action = #selector(toggle(_:))
            b.sendAction(on: [.leftMouseUp, .rightMouseUp])
        }
        store.onChange = { [weak self] in self?.updateIcon() }
        store.start()
        // ⌥⌘D toggles the menu from anywhere.
        hotKey = HotKey(keyCode: UInt32(kVK_ANSI_D), modifiers: UInt32(optionKey | cmdKey)) { [weak self] in
            Task { @MainActor in self?.toggle(nil) }
        }
        // Refresh after wake so resets and the icon are current.
        if ProcessInfo.processInfo.environment["DIAL_OPEN_ON_LAUNCH"] != nil {
            DispatchQueue.main.asyncAfter(deadline: .now() + 1.5) { [weak self] in self?.toggle(nil) }
        }
        NSWorkspace.shared.notificationCenter.addObserver(forName: NSWorkspace.didWakeNotification, object: nil, queue: .main) { [weak self] _ in
            Task { @MainActor in await self?.store.load(refresh: true) }
        }
    }

    private func updateIcon() {
        guard let b = item.button else { return }
        let c = store.activePeak("claude"), x = store.activePeak("codex")
        let alert = [c, x].compactMap { $0 }.contains { $0 >= 90 }
        b.image = StatusIcon.image(claude: c, codex: x, alert: alert)
        if store.showPercent {
            let parts = [c.map { "\(Int($0.rounded()))" }, x.map { "\(Int($0.rounded()))" }].compactMap { $0 }
            b.attributedTitle = NSAttributedString(string: parts.isEmpty ? "" : " " + parts.joined(separator: "·"),
                attributes: [.font: NSFont.monospacedDigitSystemFont(ofSize: 11, weight: .medium)])
        } else {
            b.title = ""
        }
        var tip = "Dial"
        if let st = store.status {
            for ps in st.providers {
                if let p = ps.profiles.first(where: \.active) {
                    let pct = p.maxUsed.map { " \(Int($0.rounded()))%" } ?? ""
                    tip += "\n\(ps.provider): \(p.label)\(pct)"
                }
            }
        }
        b.toolTip = tip
    }

    @objc private func toggle(_ sender: Any?) {
        if let p = popover, p.isShown {
            p.performClose(sender)
            return
        }
        let p = popover ?? makePopover()
        popover = p
        guard let b = item.button else { return }
        Task { await store.load(refresh: false) }
        p.show(relativeTo: b.bounds, of: b, preferredEdge: .minY)
        p.contentViewController?.view.window?.makeKey()
        NSApp.activate(ignoringOtherApps: true)
        // Fetch anything older than the CLI's usage TTL (cheap when fresh).
        Task { await store.load(refresh: true) }
    }

    private func makePopover() -> NSPopover {
        let p = NSPopover()
        p.behavior = .transient
        p.animates = true
        p.delegate = self
        let view = MenuView(store: store, quit: { NSApp.terminate(nil) }, openSettings: {})
        let host = NSHostingController(rootView: view)
        host.sizingOptions = [.preferredContentSize]
        p.contentViewController = host
        return p
    }

    func popoverDidClose(_ notification: Notification) {
        // Drop the SwiftUI tree while closed to keep the idle footprint small.
        popover = nil
    }
}
