import AppKit

/// Builds one small launcher app per desktop account in ~/Applications/Dial
/// ("Claude · work.app"), each with its own icon. Pinned in the Dock, a
/// launcher always brings *its* account's window forward (or opens it), so
/// the Dock never depends on which Claude instance happened to start first.
enum Launchers {
    static let folder = URL(fileURLWithPath: NSHomeDirectory()).appendingPathComponent("Applications/Dial", isDirectory: true)
    static let idPrefix = "io.github.medpar.dial.launch."

    struct Spec {
        var target: String   // "claude:work"
        var name: String     // "Claude · work"
        var short: String    // "work"
        var provider: String
        var index: Int
    }

    /// Accounts that have a desktop presence, from `dial status --json`.
    static func specs() -> [Spec] {
        let sema = DispatchSemaphore(value: 0)
        var status: DialStatus?
        Task.detached {
            status = try? await DialClient.status(refresh: false)
            sema.signal()
        }
        sema.wait()
        guard let st = status else { return [] }
        var out: [Spec] = []
        for ps in st.providers {
            for (i, p) in ps.profiles.enumerated() {
                let hasDesktop = ps.provider == "claude" ? p.desktop != nil : p.loggedIn
                guard hasDesktop else { continue }
                let short = shortName(provider: ps.provider, p)
                let prov = ps.provider == "claude" ? "Claude" : "Codex"
                out.append(Spec(target: "\(ps.provider):\(p.id)", name: "\(prov) · \(short)", short: short, provider: ps.provider, index: i))
            }
        }
        return out
    }

    /// A short, recognisable name: the profile's own label unless it's an
    /// email, then a team's organisation name, then the profile id.
    static func shortName(provider: String, _ p: ProfileStatus) -> String {
        if !p.label.contains("@") { return p.label }
        if provider == "claude", let org = p.org, !org.isEmpty, !org.contains("@"), !org.lowercased().contains("organization") {
            return org
        }
        return p.id
    }

    @discardableResult
    static func makeAll(pin: Bool, replace: Bool = false, log: (String) -> Void = { print($0) }) -> [URL] {
        let fm = FileManager.default
        let template = Bundle.main.bundleURL.appendingPathComponent("Contents/Helpers/DialLauncher.app")
        guard fm.fileExists(atPath: template.path) else {
            log("Dial.app has no launcher template; reinstall Dial")
            return []
        }
        try? fm.createDirectory(at: folder, withIntermediateDirectories: true)
        let specs = specs()
        var made: [URL] = []
        for s in specs {
            let dest = folder.appendingPathComponent("\(s.name).app")
            do {
                try make(spec: s, from: template, to: dest)
                made.append(dest)
                log("✓ \(dest.lastPathComponent)")
            } catch {
                log("✗ \(s.name): \(error.localizedDescription)")
            }
        }
        // Remove launchers for accounts that are gone.
        let keep = Set(made.map(\.lastPathComponent))
        for url in (try? fm.contentsOfDirectory(at: folder, includingPropertiesForKeys: nil)) ?? [] where url.pathExtension == "app" && !keep.contains(url.lastPathComponent) {
            if let id = Bundle(url: url)?.bundleIdentifier, id.hasPrefix(idPrefix) {
                try? fm.removeItem(at: url)
            }
        }
        if pin { addToDock(made, replace: replace) }
        return made
    }

    static func make(spec s: Spec, from template: URL, to dest: URL) throws {
        let fm = FileManager.default
        if fm.fileExists(atPath: dest.path) { try fm.removeItem(at: dest) }
        try fm.copyItem(at: template, to: dest)
        let plistURL = dest.appendingPathComponent("Contents/Info.plist")
        let data = try Data(contentsOf: plistURL)
        guard var plist = try PropertyListSerialization.propertyList(from: data, format: nil) as? [String: Any] else {
            throw NSError(domain: "dial", code: 1, userInfo: [NSLocalizedDescriptionKey: "bad template plist"])
        }
        let slug = s.target.lowercased().map { $0.isLetter || $0.isNumber ? $0 : "-" }
        plist["CFBundleIdentifier"] = idPrefix + String(slug)
        plist["CFBundleName"] = s.name
        plist["CFBundleDisplayName"] = s.name
        plist["DialTarget"] = s.target
        plist["CFBundleIconFile"] = "AppIcon"
        let out = try PropertyListSerialization.data(fromPropertyList: plist, format: .xml, options: 0)
        try out.write(to: plistURL)
        try writeIcon(spec: s, to: dest.appendingPathComponent("Contents/Resources/AppIcon.icns"))
        _ = shell("/usr/bin/codesign", ["--force", "--sign", "-", dest.path])
        // Make Finder/Dock pick up the new icon and name.
        _ = shell("/usr/bin/touch", [dest.path])
    }

    // MARK: icon

    static func hue(for s: Spec) -> NSColor {
        // Warm hues for Claude, cool for Codex; neighbours stay distinct.
        let warm: [(CGFloat, CGFloat, CGFloat)] = [(0.85, 0.47, 0.34), (0.80, 0.33, 0.42), (0.88, 0.62, 0.25), (0.70, 0.38, 0.62), (0.72, 0.30, 0.25)]
        let cool: [(CGFloat, CGFloat, CGFloat)] = [(0.16, 0.66, 0.60), (0.25, 0.50, 0.90), (0.40, 0.70, 0.30), (0.45, 0.40, 0.85), (0.10, 0.55, 0.75)]
        let set = s.provider == "claude" ? warm : cool
        let c = set[s.index % set.count]
        return NSColor(calibratedRed: c.0, green: c.1, blue: c.2, alpha: 1)
    }

    static func render(_ s: Spec, px: Int) -> Data? {
        let size = CGFloat(px)
        guard let rep = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: px, pixelsHigh: px, bitsPerSample: 8,
                                         samplesPerPixel: 4, hasAlpha: true, isPlanar: false, colorSpaceName: .deviceRGB,
                                         bytesPerRow: 0, bitsPerPixel: 0) else { return nil }
        NSGraphicsContext.saveGraphicsState()
        defer { NSGraphicsContext.restoreGraphicsState() }
        guard let ctx = NSGraphicsContext(bitmapImageRep: rep) else { return nil }
        NSGraphicsContext.current = ctx
        let g = ctx.cgContext
        let inset = size * 100 / 1024
        let body = CGRect(x: inset, y: inset * 1.1, width: size - inset * 2, height: size - inset * 2)
        let path = CGPath(roundedRect: body, cornerWidth: body.width * 0.225, cornerHeight: body.width * 0.225, transform: nil)
        let base = hue(for: s)
        g.saveGState()
        g.setShadow(offset: CGSize(width: 0, height: -size * 0.01), blur: size * 0.025, color: NSColor.black.withAlphaComponent(0.3).cgColor)
        g.addPath(path)
        g.setFillColor(base.cgColor)
        g.fillPath()
        g.restoreGState()
        g.saveGState()
        g.addPath(path)
        g.clip()
        let top = base.blended(withFraction: 0.18, of: .white) ?? base
        let bottom = base.blended(withFraction: 0.22, of: .black) ?? base
        let grad = CGGradient(colorsSpace: CGColorSpaceCreateDeviceRGB(), colors: [top.cgColor, bottom.cgColor] as CFArray, locations: [0, 1])!
        g.drawLinearGradient(grad, start: CGPoint(x: body.midX, y: body.maxY), end: CGPoint(x: body.midX, y: body.minY), options: [])
        // Provider band at the bottom.
        let band = CGRect(x: body.minX, y: body.minY, width: body.width, height: body.height * 0.24)
        g.setFillColor(NSColor.black.withAlphaComponent(0.22).cgColor)
        g.fill(band)
        g.restoreGState()

        let letter = String(s.short.prefix(1)).uppercased()
        let font = NSFont.systemFont(ofSize: body.height * 0.42, weight: .bold).rounded()
        let attrs: [NSAttributedString.Key: Any] = [.font: font, .foregroundColor: NSColor.white]
        let ls = (letter as NSString).size(withAttributes: attrs)
        (letter as NSString).draw(at: CGPoint(x: body.midX - ls.width / 2, y: body.minY + body.height * 0.27), withAttributes: attrs)

        // Provider name across the top so Claude and Codex icons never mix up.
        let pname = s.provider == "claude" ? "CLAUDE" : "CODEX"
        let pfont = NSFont.systemFont(ofSize: body.height * 0.085, weight: .heavy)
        let pattrs: [NSAttributedString.Key: Any] = [.font: pfont, .foregroundColor: NSColor.white.withAlphaComponent(0.78), .kern: body.height * 0.012]
        let psz = (pname as NSString).size(withAttributes: pattrs)
        (pname as NSString).draw(at: CGPoint(x: body.midX - psz.width / 2, y: body.maxY - body.height * 0.16), withAttributes: pattrs)

        let caption = s.short.count > 10 ? String(s.short.prefix(9)) + "…" : s.short
        let cfont = NSFont.systemFont(ofSize: body.height * 0.115, weight: .semibold)
        let cattrs: [NSAttributedString.Key: Any] = [.font: cfont, .foregroundColor: NSColor.white.withAlphaComponent(0.95)]
        let cs = (caption as NSString).size(withAttributes: cattrs)
        (caption as NSString).draw(at: CGPoint(x: body.midX - cs.width / 2, y: body.minY + body.height * 0.065), withAttributes: cattrs)
        return rep.representation(using: .png, properties: [:])
    }

    static func writeIcon(spec s: Spec, to icns: URL) throws {
        let fm = FileManager.default
        let tmp = fm.temporaryDirectory.appendingPathComponent("dial-icon-\(UUID().uuidString).iconset")
        try fm.createDirectory(at: tmp, withIntermediateDirectories: true)
        defer { try? fm.removeItem(at: tmp) }
        for (name, px) in [("16x16", 16), ("16x16@2x", 32), ("32x32", 32), ("32x32@2x", 64), ("128x128", 128),
                           ("128x128@2x", 256), ("256x256", 256), ("256x256@2x", 512), ("512x512", 512), ("512x512@2x", 1024)] {
            if let png = render(s, px: px) {
                try png.write(to: tmp.appendingPathComponent("icon_\(name).png"))
            }
        }
        try? fm.removeItem(at: icns)
        _ = shell("/usr/bin/iconutil", ["-c", "icns", tmp.path, "-o", icns.path])
    }

    // MARK: Dock

    /// Adds launchers to the Dock's pinned apps. With `replace`, the generic
    /// Claude and ChatGPT/Codex icons are taken out, so every Dock icon maps
    /// to exactly one account.
    static func addToDock(_ apps: [URL], replace: Bool = false) {
        let key = "persistent-apps" as CFString
        let domain = "com.apple.dock" as CFString
        var tiles = (CFPreferencesCopyAppValue(key, domain) as? [[String: Any]]) ?? []
        func url(of tile: [String: Any]) -> String {
            ((tile["tile-data"] as? [String: Any])?["file-data"] as? [String: Any])?["_CFURLString"] as? String ?? ""
        }
        var changed = false
        if replace {
            let before = tiles.count
            tiles.removeAll { t in
                let u = url(of: t)
                return u.hasSuffix("/Claude.app/") || u.hasSuffix("/ChatGPT.app/") || u.hasSuffix("/Codex.app/")
            }
            changed = tiles.count != before
        }
        let present = Set(tiles.map(url(of:)))
        for app in apps where !present.contains(app.absoluteString) {
            tiles.append(["tile-data": ["file-data": ["_CFURLString": app.absoluteString, "_CFURLStringType": 15],
                                        "file-label": app.deletingPathExtension().lastPathComponent],
                          "tile-type": "file-tile"])
            changed = true
        }
        guard changed else { return }
        CFPreferencesSetAppValue(key, tiles as CFArray, domain)
        CFPreferencesAppSynchronize(domain)
        _ = shell("/usr/bin/killall", ["Dock"])
    }

    @discardableResult
    static func shell(_ path: String, _ args: [String]) -> String? {
        let p = Process()
        p.executableURL = URL(fileURLWithPath: path)
        p.arguments = args
        let out = Pipe()
        p.standardOutput = out
        p.standardError = FileHandle.nullDevice
        do { try p.run() } catch { return nil }
        let data = out.fileHandleForReading.readDataToEndOfFile()
        p.waitUntilExit()
        return String(data: data, encoding: .utf8)
    }
}

/// Brings one specific process to the front, even when several instances of
/// the same app run.
enum Activator {
    @discardableResult
    static func activate(pid: Int32) -> Bool {
        guard let app = NSRunningApplication(processIdentifier: pid) else { return false }
        NSApp.yieldActivation(to: app)
        return app.activate(options: [.activateAllWindows])
    }
}

extension NSFont {
    func rounded() -> NSFont {
        guard let d = fontDescriptor.withDesign(.rounded) else { return self }
        return NSFont(descriptor: d, size: pointSize) ?? self
    }
}
