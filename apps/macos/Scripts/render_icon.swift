import AppKit

guard CommandLine.arguments.count == 2 else {
    fputs("usage: render_icon.swift <output.png>\n", stderr)
    exit(64)
}

let outputURL = URL(fileURLWithPath: CommandLine.arguments[1])
let size = NSSize(width: 1024, height: 1024)
let image = NSImage(size: size)

image.lockFocus()

let rect = NSRect(origin: .zero, size: size)
NSColor.clear.setFill()
rect.fill()

let shellRect = rect.insetBy(dx: 72, dy: 72)
let shellPath = NSBezierPath(roundedRect: shellRect, xRadius: 224, yRadius: 224)
let gradient = NSGradient(colors: [
    NSColor(calibratedRed: 0.08, green: 0.12, blue: 0.15, alpha: 1),
    NSColor(calibratedRed: 0.06, green: 0.33, blue: 0.42, alpha: 1),
    NSColor(calibratedRed: 0.12, green: 0.60, blue: 0.47, alpha: 1),
])!
gradient.draw(in: shellPath, angle: -42)

NSColor.white.withAlphaComponent(0.18).setStroke()
shellPath.lineWidth = 10
shellPath.stroke()

let cardRect = NSRect(x: 206, y: 216, width: 612, height: 592)
let cardPath = NSBezierPath(roundedRect: cardRect, xRadius: 96, yRadius: 96)
NSColor.black.withAlphaComponent(0.16).setFill()
cardPath.fill()
NSColor.white.withAlphaComponent(0.14).setStroke()
cardPath.lineWidth = 6
cardPath.stroke()

func drawRail(y: CGFloat, width: CGFloat, fill: CGFloat, color: NSColor) {
    let track = NSBezierPath(roundedRect: NSRect(x: 300, y: y, width: width, height: 34), xRadius: 17, yRadius: 17)
    NSColor.white.withAlphaComponent(0.18).setFill()
    track.fill()

    let fillPath = NSBezierPath(roundedRect: NSRect(x: 300, y: y, width: max(34, width * fill), height: 34), xRadius: 17, yRadius: 17)
    color.setFill()
    fillPath.fill()
}

drawRail(y: 378, width: 424, fill: 0.24, color: NSColor(calibratedRed: 0.43, green: 0.88, blue: 0.74, alpha: 1))
drawRail(y: 468, width: 424, fill: 0.58, color: NSColor(calibratedRed: 0.47, green: 0.72, blue: 1.00, alpha: 1))

let curve = NSBezierPath()
curve.move(to: NSPoint(x: 316, y: 594))
curve.curve(
    to: NSPoint(x: 724, y: 596),
    controlPoint1: NSPoint(x: 422, y: 714),
    controlPoint2: NSPoint(x: 582, y: 488))
curve.lineCapStyle = .round
curve.lineJoinStyle = .round
curve.lineWidth = 38
NSColor.white.withAlphaComponent(0.92).setStroke()
curve.stroke()

let dot = NSBezierPath(ovalIn: NSRect(x: 666, y: 656, width: 70, height: 70))
NSColor(calibratedRed: 1.0, green: 0.68, blue: 0.25, alpha: 1).setFill()
dot.fill()

image.unlockFocus()

guard let tiff = image.tiffRepresentation,
      let bitmap = NSBitmapImageRep(data: tiff),
      let png = bitmap.representation(using: .png, properties: [:])
else {
    fputs("failed to render icon png\n", stderr)
    exit(1)
}

try png.write(to: outputURL)
