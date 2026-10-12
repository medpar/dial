// Renders the Dial app icon into an .iconset: a graphite squircle with two
// concentric gauge arcs (Claude orange outside, Codex teal inside) and a
// knob. Usage: swift scripts/make-icon.swift <out.iconset>
import AppKit

let out = CommandLine.arguments.dropFirst().first ?? "Dial.iconset"
try? FileManager.default.createDirectory(atPath: out, withIntermediateDirectories: true)

func render(_ px: Int) -> Data {
    let s = CGFloat(px)
    let rep = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: px, pixelsHigh: px, bitsPerSample: 8,
                               samplesPerPixel: 4, hasAlpha: true, isPlanar: false, colorSpaceName: .deviceRGB,
                               bytesPerRow: 0, bitsPerPixel: 0)!
    NSGraphicsContext.saveGraphicsState()
    let ctx = NSGraphicsContext(bitmapImageRep: rep)!
    NSGraphicsContext.current = ctx
    let g = ctx.cgContext

    // macOS icon grid: 824/1024 body with a soft drop shadow.
    let inset = s * 100 / 1024
    let body = CGRect(x: inset, y: inset * 1.12, width: s - inset * 2, height: s - inset * 2)
    let radius = body.width * 0.225
    let path = CGPath(roundedRect: body, cornerWidth: radius, cornerHeight: radius, transform: nil)
    g.saveGState()
    g.setShadow(offset: CGSize(width: 0, height: -s * 0.012), blur: s * 0.03, color: NSColor.black.withAlphaComponent(0.35).cgColor)
    g.addPath(path)
    g.setFillColor(NSColor(calibratedWhite: 0.11, alpha: 1).cgColor)
    g.fillPath()
    g.restoreGState()

    g.saveGState()
    g.addPath(path)
    g.clip()
    let grad = CGGradient(colorsSpace: CGColorSpaceCreateDeviceRGB(), colors: [
        NSColor(calibratedRed: 0.19, green: 0.19, blue: 0.21, alpha: 1).cgColor,
        NSColor(calibratedRed: 0.08, green: 0.08, blue: 0.09, alpha: 1).cgColor,
    ] as CFArray, locations: [0, 1])!
    g.drawLinearGradient(grad, start: CGPoint(x: body.midX, y: body.maxY), end: CGPoint(x: body.midX, y: body.minY), options: [])
    g.restoreGState()

    let c = CGPoint(x: body.midX, y: body.midY)
    let start: CGFloat = 225 * .pi / 180
    let sweep: CGFloat = 270 * .pi / 180
    func arc(_ r: CGFloat, _ w: CGFloat, _ frac: CGFloat, _ color: NSColor) {
        g.setLineWidth(w)
        g.setLineCap(.round)
        g.setStrokeColor(color.cgColor)
        g.addArc(center: c, radius: r, startAngle: start, endAngle: start - sweep * frac, clockwise: true)
        g.strokePath()
    }
    let unit = body.width
    let orange = NSColor(calibratedRed: 0.85, green: 0.47, blue: 0.34, alpha: 1)
    let teal = NSColor(calibratedRed: 0.16, green: 0.72, blue: 0.64, alpha: 1)
    arc(unit * 0.33, unit * 0.072, 1, NSColor.white.withAlphaComponent(0.09))
    arc(unit * 0.205, unit * 0.072, 1, NSColor.white.withAlphaComponent(0.09))
    arc(unit * 0.33, unit * 0.072, 0.72, orange)
    arc(unit * 0.205, unit * 0.072, 0.46, teal)
    // Knob with a pointer notch
    let kr = unit * 0.085
    g.saveGState()
    g.setShadow(offset: CGSize(width: 0, height: -unit * 0.008), blur: unit * 0.02, color: NSColor.black.withAlphaComponent(0.5).cgColor)
    g.setFillColor(NSColor(calibratedWhite: 0.96, alpha: 1).cgColor)
    g.fillEllipse(in: CGRect(x: c.x - kr, y: c.y - kr, width: kr * 2, height: kr * 2))
    g.restoreGState()
    let ang = start - sweep * 0.72
    g.setStrokeColor(NSColor(calibratedWhite: 0.15, alpha: 1).cgColor)
    g.setLineWidth(unit * 0.018)
    g.setLineCap(.round)
    g.move(to: CGPoint(x: c.x + cos(ang) * kr * 0.2, y: c.y + sin(ang) * kr * 0.2))
    g.addLine(to: CGPoint(x: c.x + cos(ang) * kr * 0.72, y: c.y + sin(ang) * kr * 0.72))
    g.strokePath()

    NSGraphicsContext.restoreGraphicsState()
    return rep.representation(using: .png, properties: [:])!
}

for (name, px) in [("16x16", 16), ("16x16@2x", 32), ("32x32", 32), ("32x32@2x", 64), ("128x128", 128),
                   ("128x128@2x", 256), ("256x256", 256), ("256x256@2x", 512), ("512x512", 512), ("512x512@2x", 1024)] {
    try! render(px).write(to: URL(fileURLWithPath: "\(out)/icon_\(name).png"))
}
print("wrote \(out)")
