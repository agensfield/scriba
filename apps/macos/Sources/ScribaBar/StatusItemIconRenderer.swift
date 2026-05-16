import AppKit

enum StatusItemIconRenderer {
    private static let size = NSSize(width: 18, height: 18)

    static func makeIcon(primaryPercent: Double?, secondaryPercent: Double?, isStale: Bool) -> NSImage {
        let image = NSImage(size: size)
        image.lockFocus()
        defer {
            image.unlockFocus()
            image.isTemplate = true
        }

        NSColor.clear.setFill()
        NSBezierPath(rect: NSRect(origin: .zero, size: size)).fill()

        let alpha: CGFloat = isStale ? 0.58 : 1
        drawGlyphBase(alpha: alpha)
        drawBar(y: 4.5, percent: primaryPercent, alpha: alpha)
        drawBar(y: 11, percent: secondaryPercent, alpha: alpha * 0.86)

        return image
    }

    private static func drawGlyphBase(alpha: CGFloat) {
        let mark = NSBezierPath(roundedRect: NSRect(x: 2.5, y: 2.5, width: 13, height: 13), xRadius: 3.5, yRadius: 3.5)
        mark.lineWidth = 1.2
        NSColor.labelColor.withAlphaComponent(0.24 * alpha).setStroke()
        mark.stroke()

        let stem = NSBezierPath()
        stem.move(to: NSPoint(x: 5.7, y: 14))
        stem.curve(
            to: NSPoint(x: 12.6, y: 4),
            controlPoint1: NSPoint(x: 13.5, y: 14.2),
            controlPoint2: NSPoint(x: 4.2, y: 4.2))
        stem.lineWidth = 1.3
        stem.lineCapStyle = .round
        NSColor.labelColor.withAlphaComponent(0.82 * alpha).setStroke()
        stem.stroke()
    }

    private static func drawBar(y: CGFloat, percent: Double?, alpha: CGFloat) {
        let rect = NSRect(x: 4, y: y, width: 10, height: 2.2)
        let track = NSBezierPath(roundedRect: rect, xRadius: 1.1, yRadius: 1.1)
        NSColor.labelColor.withAlphaComponent(0.20 * alpha).setFill()
        track.fill()

        guard let percent else { return }
        let clamped = min(max(percent / 100, 0), 1)
        guard clamped > 0 else { return }

        let fillRect = NSRect(x: rect.minX, y: rect.minY, width: max(1.1, rect.width * clamped), height: rect.height)
        let fill = NSBezierPath(roundedRect: fillRect, xRadius: 1.1, yRadius: 1.1)
        NSColor.labelColor.withAlphaComponent(0.95 * alpha).setFill()
        fill.fill()
    }
}
