import AppKit

/// The menu bar glyph: a dial with two concentric gauge arcs. The outer arc
/// is the active Claude account's peak usage, the inner one Codex's; the
/// centre dot is the knob. Drawn as a template image so it follows the
/// menu bar's light/dark appearance.
enum StatusIcon {
    static let size = NSSize(width: 18, height: 18)
    // Gauge sweep: 270°, open at the bottom like a physical dial.
    private static let start: CGFloat = 225
    private static let sweep: CGFloat = 270

    static func image(claude: Double?, codex: Double?, alert: Bool = false) -> NSImage {
        let img = NSImage(size: size, flipped: false) { rect in
            let c = NSPoint(x: rect.midX, y: rect.midY)
            func arc(radius: CGFloat, width: CGFloat, fraction: CGFloat, alpha: CGFloat) {
                guard fraction > 0 else { return }
                let p = NSBezierPath()
                p.appendArc(withCenter: c, radius: radius, startAngle: start, endAngle: start - sweep * min(fraction, 1), clockwise: true)
                p.lineWidth = width
                p.lineCapStyle = .round
                NSColor.black.withAlphaComponent(alpha).setStroke()
                p.stroke()
            }
            // Tracks
            arc(radius: 7.6, width: 1.6, fraction: 1, alpha: 0.28)
            arc(radius: 4.4, width: 1.6, fraction: 1, alpha: 0.28)
            // Values (a tiny minimum keeps a fresh account visible)
            if let v = claude { arc(radius: 7.6, width: 1.9, fraction: max(CGFloat(v) / 100, 0.04), alpha: 1) }
            if let v = codex { arc(radius: 4.4, width: 1.9, fraction: max(CGFloat(v) / 100, 0.04), alpha: 1) }
            // Knob
            let r: CGFloat = alert ? 1.9 : 1.4
            NSColor.black.setFill()
            NSBezierPath(ovalIn: NSRect(x: c.x - r, y: c.y - r, width: r * 2, height: r * 2)).fill()
            return true
        }
        img.isTemplate = true
        img.accessibilityDescription = "Dial"
        return img
    }
}
