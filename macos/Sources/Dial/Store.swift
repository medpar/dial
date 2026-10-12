import Foundation
import SwiftUI

/// App state. Reads come from cached CLI output first (instant), then a
/// background refresh fetches stale usage.
@MainActor
final class Store: ObservableObject {
    @Published var status: DialStatus?
    @Published var loading = false
    @Published var busy: String?          // "provider:id" being switched
    @Published var toast: Toast?
    @Published var lastError: String?

    struct Toast: Identifiable, Equatable {
        let id = UUID()
        var text: String
        var ok: Bool
    }

    var onChange: (() -> Void)?

    @AppStorage("refreshMinutes") var refreshMinutes: Int = 5
    @AppStorage("showPercent") var showPercent: Bool = false

    private var timer: Timer?
    private var keepAliveTimer: Timer?
    private var watcher: FileWatcher?

    func start() {
        // Account changes made outside dial (Claude Desktop sign-in, `codex
        // login`, `/login`) make remotes follow within seconds.
        let env = ProcessInfo.processInfo.environment
        let home = NSHomeDirectory()
        let codexHome = env["CODEX_HOME"] ?? "\(home)/.codex"
        let claudeData = env["DIAL_CLAUDE_DESKTOP_DATA"] ?? "\(home)/Library/Application Support/Claude"
        watcher = FileWatcher(paths: ["\(codexHome)/auth.json", "\(claudeData)/config.json"], debounce: 2) { [weak self] in
            Task { @MainActor in
                _ = try? await DialClient.run(["follow", "--json"], timeout: 60)
                await self?.load(refresh: false)
            }
        }
        Task { await load(refresh: false); await load(refresh: true) }
        schedule()
        keepAliveTimer = Timer.scheduledTimer(withTimeInterval: 6 * 3600, repeats: true) { _ in
            Task { _ = try? await DialClient.run(["keepalive", "--json"], timeout: 300) }
        }
    }

    func schedule() {
        timer?.invalidate()
        let interval = TimeInterval(max(refreshMinutes, 1) * 60)
        timer = Timer.scheduledTimer(withTimeInterval: interval, repeats: true) { [weak self] _ in
            Task { await self?.load(refresh: true) }
        }
        timer?.tolerance = interval * 0.2
    }

    func load(refresh: Bool, force: Bool = false) async {
        if refresh { loading = true }
        defer { if refresh { loading = false } }
        do {
            status = try await DialClient.status(refresh: refresh, force: force)
            lastError = nil
        } catch {
            lastError = error.localizedDescription
        }
        onChange?()
    }

    func use(_ provider: String, _ p: ProfileStatus, scope: String? = nil) async {
        busy = "\(provider):\(p.id)"
        defer { busy = nil }
        do {
            let r = try await DialClient.use(provider, p.id, scope: scope)
            var parts: [String] = []
            if r.cli == "ok" { parts.append("CLI") }
            if r.desktop.hasPrefix("ok") { parts.append("app") }
            for s in r.remotes ?? [] where s.ok { parts.append(s.remote) }
            var text = parts.isEmpty ? "" : "\(r.label) · \(parts.joined(separator: " + "))"
            var ok = r.ok
            if r.cli != "ok" && r.cli != "skipped" { text = r.cli; ok = false }
            else if !r.desktop.hasPrefix("ok") && !r.desktop.hasPrefix("skipped") && r.desktop != "" { text += (text.isEmpty ? "" : " · ") + "app: " + r.desktop; ok = false }
            if let bad = r.remotes?.first(where: { !$0.ok }) { text += " · " + (bad.message ?? bad.remote); ok = false }
            if let w = r.warnings?.first { text += " · " + w }
            show(text.isEmpty ? "Switched to \(r.label)" : text, ok: ok)
        } catch {
            show(error.localizedDescription, ok: false)
        }
        await load(refresh: false)
        Task { await load(refresh: true) }
    }

    func openWindow(_ provider: String, _ p: ProfileStatus) async {
        busy = "\(provider):\(p.id)"
        defer { busy = nil }
        do {
            let r = try await DialClient.open(provider, p.id)
            show(r.ok ? "Opened \(p.label) in its own window" : (r.message ?? "Could not open"), ok: r.ok)
        } catch {
            show(error.localizedDescription, ok: false)
        }
        await load(refresh: false)
    }

    func show(_ text: String, ok: Bool) {
        let t = Toast(text: text, ok: ok)
        toast = t
        DispatchQueue.main.asyncAfter(deadline: .now() + (ok ? 4 : 9)) { [weak self] in
            if self?.toast == t { self?.toast = nil }
        }
    }

    /// Peak usage of the active account of a provider, for the icon.
    func activePeak(_ provider: String) -> Double? {
        status?.provider(provider)?.profiles.first(where: \.active)?.maxUsed
    }
}
