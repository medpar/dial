import SwiftUI

enum Palette {
    static let claude = Color(red: 0.85, green: 0.47, blue: 0.34)
    static let codex = Color(red: 0.16, green: 0.69, blue: 0.62)
    static func accent(_ provider: String) -> Color { provider == "claude" ? claude : codex }
    static func level(_ pct: Double, _ provider: String) -> Color {
        if pct >= 90 { return Color(red: 0.92, green: 0.30, blue: 0.28) }
        if pct >= 75 { return Color(red: 0.95, green: 0.62, blue: 0.20) }
        return accent(provider)
    }
}

enum Fmt {
    static func until(_ date: Date) -> String {
        let s = Int(date.timeIntervalSinceNow)
        if s <= 0 { return "now" }
        if s < 3600 { return "\(s / 60 + 1)m" }
        if s < 86400 { return "\(s / 3600)h\(String(format: "%02d", (s % 3600) / 60))m" }
        return "\(s / 86400)d\((s % 86400) / 3600)h"
    }
    static func ago(_ date: Date) -> String {
        let s = Int(-date.timeIntervalSinceNow)
        if s < 60 { return "just now" }
        if s < 3600 { return "\(s / 60)m ago" }
        if s < 172800 { return "\(s / 3600)h ago" }
        return "\(s / 86400)d ago"
    }
    static let when: DateFormatter = {
        let f = DateFormatter()
        f.dateFormat = "EEE HH:mm"
        return f
    }()
    static func short(_ label: String) -> String {
        switch label {
        case "5h session": return "5h"
        case "Weekly": return "Week"
        default:
            if label.hasPrefix("Weekly · ") { return String(label.dropFirst(9)) }
            if label.hasPrefix("Code review") { return "Review" }
            return label
        }
    }
}

struct MenuView: View {
    @ObservedObject var store: Store
    var quit: () -> Void
    var openSettings: () -> Void

    var body: some View {
        VStack(spacing: 0) {
            header
            Divider().opacity(0.5)
            if let st = store.status {
                ScrollView {
                    VStack(alignment: .leading, spacing: 14) {
                        ForEach(st.providers) { ps in
                            ProviderSection(store: store, ps: ps, desktop: ps.provider == "claude" ? st.desktop.claude : st.desktop.codex)
                        }
                        if st.providers.allSatisfy({ $0.profiles.isEmpty }) { emptyState }
                    }
                    .padding(.horizontal, 12)
                    .padding(.vertical, 12)
                }
                .frame(maxHeight: 520)
                footer(st)
            } else if let err = store.lastError {
                Text(err).font(.system(size: 12)).foregroundStyle(.secondary).padding(20)
            } else {
                ProgressView().controlSize(.small).padding(30)
            }
        }
        .frame(width: 348)
        .focusEffectDisabled()
    }

    private var header: some View {
        HStack(spacing: 8) {
            Image(nsImage: StatusIcon.image(claude: store.activePeak("claude"), codex: store.activePeak("codex")))
                .renderingMode(.template)
            Text("Dial").font(.system(size: 13, weight: .semibold))
            Spacer()
            if let st = store.status {
                Text(store.loading ? "updating…" : Fmt.ago(st.generatedAt))
                    .font(.system(size: 11)).foregroundStyle(.tertiary).monospacedDigit()
            }
            Button { Task { await store.load(refresh: true, force: true) } } label: {
                Image(systemName: "arrow.clockwise").font(.system(size: 11, weight: .medium))
                    .rotationEffect(.degrees(store.loading ? 360 : 0))
                    .animation(store.loading ? .linear(duration: 1).repeatForever(autoreverses: false) : .default, value: store.loading)
            }
            .buttonStyle(IconButtonStyle()).focusable(false).help("Refresh usage")
            Menu {
                Menu("Add account") {
                    Button("Claude (CLI login)…") { DialClient.openInTerminal(["add", "claude"], title: "dial-add-claude") }
                    Button("Claude Desktop account…") { DesktopSignin.start(account: nil, label: nil) }
                    Button("Codex…") { DialClient.openInTerminal(["add", "codex"], title: "dial-add-codex") }
                }
                Button("Import current logins") { Task { _ = try? await DialClient.run(["import"]); await store.load(refresh: true) } }
                Divider()
                Picker("Refresh every", selection: Binding(get: { store.refreshMinutes }, set: { store.refreshMinutes = $0; store.schedule() })) {
                    ForEach([2, 5, 10, 15, 30], id: \.self) { Text("\($0) min").tag($0) }
                }
                Toggle("Show % in menu bar", isOn: Binding(get: { store.showPercent }, set: { store.showPercent = $0; store.onChange?() }))
                Toggle("Launch at login", isOn: Binding(get: { LoginItem.enabled }, set: { LoginItem.set($0) }))
                Divider()
                Button("Open terminal status") { DialClient.openInTerminal(["watch"], title: "dial-watch") }
                Button("Reveal ~/.dial") { NSWorkspace.shared.open(URL(fileURLWithPath: NSHomeDirectory() + "/.dial")) }
                Divider()
                Button("Quit Dial") { quit() }
            } label: {
                Image(systemName: "ellipsis.circle").font(.system(size: 12))
            }
            .menuStyle(.borderlessButton).menuIndicator(.hidden).fixedSize().focusable(false)
        }
        .padding(.horizontal, 12).padding(.vertical, 9)
    }

    private var emptyState: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text("No accounts yet").font(.system(size: 12, weight: .semibold))
            Text("Import the accounts you're logged into, or add more from the ⋯ menu.")
                .font(.system(size: 11)).foregroundStyle(.secondary)
            Button("Import current logins") { Task { _ = try? await DialClient.run(["import"]); await store.load(refresh: true) } }
                .controlSize(.small)
        }
    }

    @ViewBuilder
    private func footer(_ st: DialStatus) -> some View {
        let remotes = st.remotes ?? []
        if !remotes.isEmpty || store.toast != nil {
            Divider().opacity(0.5)
            VStack(alignment: .leading, spacing: 6) {
                if let t = store.toast {
                    HStack(alignment: .top, spacing: 6) {
                        Image(systemName: t.ok ? "checkmark.circle.fill" : "exclamationmark.triangle.fill")
                            .foregroundStyle(t.ok ? Color.green : Color.orange).font(.system(size: 11))
                        Text(t.text).font(.system(size: 11)).fixedSize(horizontal: false, vertical: true)
                    }
                    .transition(.opacity)
                }
                ForEach(remotes) { r in RemoteLine(r: r) }
            }
            .padding(.horizontal, 12).padding(.vertical, 9)
            .frame(maxWidth: .infinity, alignment: .leading)
        }
    }
}

struct RemoteLine: View {
    var r: RemoteStatus

    /// Extracts the suggested `dial …` command from a sync message.
    static func command(in message: String?) -> [String]? {
        guard let m = message, let a = m.range(of: "`dial "), let b = m[a.upperBound...].firstIndex(of: "`") else { return nil }
        return m[a.upperBound..<b].split(separator: " ").map(String.init)
    }
    var body: some View {
        HStack(spacing: 6) {
            Image(systemName: "server.rack").font(.system(size: 10)).foregroundStyle(.secondary)
            Text(r.name).font(.system(size: 11, weight: .medium))
            let results = ["claude", "codex"].compactMap { r.sync?[$0] }
            if results.isEmpty {
                Text(r.auto ? "follows switches" : "manual").font(.system(size: 11)).foregroundStyle(.tertiary)
            }
            ForEach(Array(results.enumerated()), id: \.offset) { _, s in
                HStack(spacing: 3) {
                    Circle().fill(s.ok ? Color.green : Color.orange).frame(width: 5, height: 5)
                    Text(s.ok ? (s.message ?? "") : "needs login").font(.system(size: 11)).foregroundStyle(.secondary).lineLimit(1)
                }
                .help(s.message ?? "")
            }
            Spacer(minLength: 0)
            if let fix = results.compactMap({ $0.ok ? nil : RemoteLine.command(in: $0.message) }).first {
                Button("Set up…") { DialClient.openInTerminal(fix, title: "dial-remote-seed") }
                    .buttonStyle(.link).font(.system(size: 11))
                    .help("Give \(r.name) its own login for this account (one browser sign-in)")
            }
        }
    }
}

struct ProviderSection: View {
    @ObservedObject var store: Store
    var ps: ProviderStatus
    var desktop: DesktopStatus

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack(spacing: 6) {
                Circle().fill(Palette.accent(ps.provider)).frame(width: 7, height: 7)
                Text(ps.provider == "claude" ? "CLAUDE" : "CODEX")
                    .font(.system(size: 10, weight: .semibold)).tracking(0.8).foregroundStyle(.secondary)
                Spacer()
                if desktop.installed {
                    Text(desktop.running ? "app open" : "app closed")
                        .font(.system(size: 10)).foregroundStyle(.tertiary)
                }
            }
            .padding(.horizontal, 4)
            if ps.profiles.isEmpty {
                Button("Add a \(ps.provider == "claude" ? "Claude" : "Codex") account…") {
                    DialClient.openInTerminal(["add", ps.provider], title: "dial-add-\(ps.provider)")
                }
                .buttonStyle(.link).font(.system(size: 11)).padding(.horizontal, 4)
            }
            ForEach(ps.profiles) { p in
                AccountRow(store: store, provider: ps.provider, p: p, desktopInstalled: desktop.installed)
            }
        }
    }
}

struct AccountRow: View {
    @ObservedObject var store: Store
    var provider: String
    var p: ProfileStatus
    var desktopInstalled: Bool
    @State private var hover = false
    @State private var expanded = false

    private var busy: Bool { store.busy == "\(provider):\(p.id)" }

    var body: some View {
        VStack(alignment: .leading, spacing: 5) {
            HStack(spacing: 7) {
                ZStack {
                    Circle().strokeBorder(p.active ? Palette.accent(provider) : Color.secondary.opacity(0.35), lineWidth: 1.3)
                        .frame(width: 12, height: 12)
                    if p.active { Circle().fill(Palette.accent(provider)).frame(width: 6, height: 6) }
                }
                VStack(alignment: .leading, spacing: 1) {
                    Text(p.label).font(.system(size: 12.5, weight: p.active ? .semibold : .regular)).lineLimit(1).truncationMode(.middle)
                    Text(subtitle).font(.system(size: 10.5)).foregroundStyle(.secondary).lineLimit(1)
                }
                Spacer(minLength: 4)
                if busy {
                    ProgressView().controlSize(.mini)
                } else if hover {
                    actions
                } else {
                    badges
                }
            }
            if !p.loggedIn {
                Text("Logged out — renew with `dial login \(provider):\(p.id)`").font(.system(size: 10.5)).foregroundStyle(.orange).padding(.leading, 19)
            } else if let u = p.usage {
                UsageBlock(provider: provider, usage: u, expanded: expanded).padding(.leading, 19)
            } else {
                Text("usage not fetched yet").font(.system(size: 10.5)).foregroundStyle(.tertiary).padding(.leading, 19)
            }
        }
        .padding(.horizontal, 6).padding(.vertical, 6)
        .background(RoundedRectangle(cornerRadius: 7, style: .continuous).fill(hover ? Color.primary.opacity(0.06) : .clear))
        .contentShape(Rectangle())
        .onHover { hover = $0 }
        .onTapGesture {
            if p.active { withAnimation(.snappy(duration: 0.2)) { expanded.toggle() } }
            else { Task { await store.use(provider, p) } }
        }
        .help(p.active ? "Active — click for details" : "Click to switch \(provider == "claude" ? "Claude" : "Codex") to \(p.label)")
        .contextMenu { menuItems }
    }

    private var subtitle: String {
        var parts: [String] = []
        if let org = p.org, !org.isEmpty, provider == "claude" { parts.append(org) }
        if let plan = p.plan, !plan.isEmpty { parts.append(plan) }
        if p.label != p.email, let e = p.email { parts.insert(e, at: 0) }
        return parts.joined(separator: " · ")
    }

    // Where this account is in use: the CLI, the app's main window, or a
    // window of its own. Shown as explicit tags so they can't be confused.
    @ViewBuilder private var badges: some View {
        HStack(spacing: 4) {
            if p.active { Tag(text: "CLI", systemImage: "terminal") }
            if desktopInstalled {
                if p.desktop == "live" { Tag(text: provider == "claude" ? "main window" : "app", systemImage: "macwindow") }
                if p.window == true { Tag(text: "own window", systemImage: "macwindow.on.rectangle") }
            }
        }
    }

    private var needsDesktopSignin: Bool { provider == "claude" && desktopInstalled && p.desktop == nil }

    @ViewBuilder private var actions: some View {
        HStack(spacing: 2) {
            if needsDesktopSignin {
                Button { DesktopSignin.start(account: "claude:\(p.id)", label: p.label) } label: { Image(systemName: "person.badge.plus") }
                    .buttonStyle(IconButtonStyle()).help("Sign \(p.label) in to Claude Desktop (opens in its own window)")
            } else if desktopInstalled && p.desktop != "live" && p.window != true {
                Button { Task { await store.openWindow(provider, p) } } label: { Image(systemName: "macwindow.badge.plus") }
                    .buttonStyle(IconButtonStyle()).help("Open in its own \(provider == "claude" ? "Claude" : "Codex") window")
            } else if p.window == true {
                Button { Task { await store.openWindow(provider, p) } } label: { Image(systemName: "macwindow.on.rectangle") }
                    .buttonStyle(IconButtonStyle()).help("Bring its window to the front")
            }
            Button { DialClient.openInTerminal(["run", "\(provider):\(p.id)"], title: "dial-run-\(provider)-\(p.id)") } label: { Image(systemName: "terminal") }
                .buttonStyle(IconButtonStyle()).help("Run \(provider) as \(p.label) in Terminal")
            Menu { menuItems } label: { Image(systemName: "ellipsis") }
                .menuStyle(.borderlessButton).menuIndicator(.hidden).fixedSize().frame(width: 20)
        }
    }

    @ViewBuilder private var menuItems: some View {
        Button("Switch CLI + app") { Task { await store.use(provider, p) } }
        Button("Switch CLI only") { Task { await store.use(provider, p, scope: "cli") } }
        if desktopInstalled { Button("Switch desktop app only") { Task { await store.use(provider, p, scope: "desktop") } } }
        if desktopInstalled { Button("Open in its own window") { Task { await store.openWindow(provider, p) } } }
        Button("Run in Terminal") { DialClient.openInTerminal(["run", "\(provider):\(p.id)"], title: "dial-run-\(provider)-\(p.id)") }
        Divider()
        Button("Renew login…") { DialClient.openInTerminal(["login", "\(provider):\(p.id)"], title: "dial-login-\(p.id)") }
        if provider == "claude" && desktopInstalled {
            Button(p.desktop == nil ? "Sign in to Claude Desktop…" : "Renew Claude Desktop sign-in…") { DesktopSignin.start(account: "claude:\(p.id)", label: p.label) }
        }
        Button("Copy id") { NSPasteboard.general.clearContents(); NSPasteboard.general.setString("\(provider):\(p.id)", forType: .string) }
    }
}

struct Tag: View {
    var text: String
    var systemImage: String
    var body: some View {
        HStack(spacing: 3) {
            Image(systemName: systemImage).font(.system(size: 8.5))
            Text(text).font(.system(size: 9.5, weight: .medium))
        }
        .foregroundStyle(.secondary)
        .padding(.horizontal, 5).padding(.vertical, 2)
        .background(Capsule().fill(Color.primary.opacity(0.07)))
    }
}

struct UsageBlock: View {
    var provider: String
    var usage: UsageReport
    var expanded: Bool

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            let windows = usage.windows ?? []
            if windows.isEmpty, let err = usage.error {
                Text(err).font(.system(size: 10.5)).foregroundStyle(.orange).lineLimit(2)
            }
            ForEach(windows) { w in
                HStack(spacing: 7) {
                    Text(Fmt.short(w.label)).font(.system(size: 10.5)).foregroundStyle(.secondary)
                        .frame(width: 44, alignment: .leading).lineLimit(1)
                    Meter(value: w.usedPercent, color: Palette.level(w.usedPercent, provider))
                    Text("\(Int(w.usedPercent.rounded()))%").font(.system(size: 10.5, weight: .medium)).monospacedDigit()
                        .frame(width: 34, alignment: .trailing)
                    Text(w.resetsAt.map { Fmt.until($0) } ?? "—").font(.system(size: 10.5)).foregroundStyle(.tertiary).monospacedDigit()
                        .frame(width: 44, alignment: .trailing)
                        .help(w.resetsAt.map { "Resets \(Fmt.when.string(from: $0))" } ?? "Window not started")
                }
            }
            if expanded || !(usage.extra ?? []).isEmpty || usage.stale == true {
                HStack(spacing: 8) {
                    ForEach(usage.extra ?? []) { e in
                        Text("\(e.label): \(e.value)").font(.system(size: 10)).foregroundStyle(.secondary)
                    }
                    if usage.stale == true {
                        Text("stale · \(Fmt.ago(usage.fetchedAt))").font(.system(size: 10)).foregroundStyle(.orange)
                            .help(usage.error ?? "")
                    }
                }
            }
            if expanded {
                ForEach(windows.filter { $0.resetsAt != nil }) { w in
                    Text("\(w.label) resets \(Fmt.when.string(from: w.resetsAt!))").font(.system(size: 10)).foregroundStyle(.tertiary)
                }
                Text("updated \(Fmt.ago(usage.fetchedAt))").font(.system(size: 10)).foregroundStyle(.tertiary)
            }
        }
    }
}

struct Meter: View {
    var value: Double
    var color: Color
    var body: some View {
        GeometryReader { g in
            ZStack(alignment: .leading) {
                Capsule().fill(Color.primary.opacity(0.09))
                Capsule().fill(color).frame(width: max(value > 0 ? 3 : 0, g.size.width * min(value, 100) / 100))
            }
        }
        .frame(height: 5)
    }
}

struct IconButtonStyle: ButtonStyle {
    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .font(.system(size: 11.5))
            .frame(width: 22, height: 20)
            .background(RoundedRectangle(cornerRadius: 5).fill(Color.primary.opacity(configuration.isPressed ? 0.15 : 0)))
            .contentShape(Rectangle())
    }
}

/// Starts a Claude Desktop sign-in after explaining that Claude windows close
/// for a moment: the sign-in callback only reaches the right window when it
/// is the only one running.
enum DesktopSignin {
    static func start(account: String?, label: String?) {
        let alert = NSAlert()
        alert.messageText = label.map { "Sign \($0) in to Claude Desktop" } ?? "Add a Claude Desktop account"
        alert.informativeText = "Dial closes your Claude windows for a moment and opens one sign-in window, so the sign-in can't land in the wrong window. Running Claude Desktop agents pause. When you're signed in, your windows reopen and the account opens in its own window."
        alert.addButton(withTitle: "Continue")
        alert.addButton(withTitle: "Cancel")
        NSApp.activate(ignoringOtherApps: true)
        guard alert.runModal() == .alertFirstButtonReturn else { return }
        var args = ["desktop"]
        if let account { args += ["login", account] } else { args += ["add", "claude"] }
        DialClient.openInTerminal(args, title: "dial-desktop-signin")
    }
}
