// springback's favicon, redrawn at macOS app-icon geometry.
//
// The mark is a rounded tile plus one stroked path, so it is redrawn here rather than
// rasterised: every iconset size comes off the vector, and the padding stays genuinely
// transparent — Quick Look flattens SVG onto white, which shows as a white square in the Dock.
//
// Coordinates are the favicon's own 64x64 space with y flipped (AppKit is y-up, SVG is y-down),
// scaled 12.875x to an 824pt tile inside a 1024pt canvas: Apple's proportions for a macOS icon.

import Cocoa

let ink = NSColor(srgbRed: 0xE8 / 255, green: 0xEA / 255, blue: 0xED / 255, alpha: 1)
let tileColor = NSColor(srgbRed: 0x0B / 255, green: 0x0D / 255, blue: 0x10 / 255, alpha: 1)

func render(_ size: Int) -> Data {
	let rep = NSBitmapImageRep(
		bitmapDataPlanes: nil, pixelsWide: size, pixelsHigh: size,
		bitsPerSample: 8, samplesPerPixel: 4, hasAlpha: true, isPlanar: false,
		colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0)!
	NSGraphicsContext.saveGraphicsState()
	NSGraphicsContext.current = NSGraphicsContext(bitmapImageRep: rep)
	let ctx = NSGraphicsContext.current!.cgContext
	ctx.clear(CGRect(x: 0, y: 0, width: size, height: size))
	ctx.scaleBy(x: CGFloat(size) / 1024, y: CGFloat(size) / 1024)
	ctx.translateBy(x: 100, y: 100)
	ctx.scaleBy(x: 12.875, y: 12.875)

	tileColor.setFill()
	NSBezierPath(roundedRect: NSRect(x: 0, y: 0, width: 64, height: 64), xRadius: 14, yRadius: 14).fill()

	// The S, drawn as a leaf spring: two half-turns of radius 6.5 joined by flat runs.
	let s = NSBezierPath()
	s.move(to: NSPoint(x: 43, y: 45))
	s.line(to: NSPoint(x: 29, y: 45))
	s.appendArc(withCenter: NSPoint(x: 29, y: 38.5), radius: 6.5, startAngle: 90, endAngle: 270, clockwise: false)
	s.line(to: NSPoint(x: 35, y: 32))
	s.appendArc(withCenter: NSPoint(x: 35, y: 25.5), radius: 6.5, startAngle: 90, endAngle: 270, clockwise: true)
	s.line(to: NSPoint(x: 21, y: 19))
	s.lineWidth = 6.5
	s.lineCapStyle = .round
	s.lineJoinStyle = .round
	ink.setStroke()
	s.stroke()

	NSGraphicsContext.restoreGraphicsState()
	return rep.representation(using: .png, properties: [:])!
}

let out = CommandLine.arguments[1]
try? FileManager.default.createDirectory(atPath: out, withIntermediateDirectories: true)
for (name, px) in [("16x16", 16), ("16x16@2x", 32), ("32x32", 32), ("32x32@2x", 64),
                   ("128x128", 128), ("128x128@2x", 256), ("256x256", 256), ("256x256@2x", 512),
                   ("512x512", 512), ("512x512@2x", 1024)] {
	try! render(px).write(to: URL(fileURLWithPath: "\(out)/icon_\(name).png"))
}
print("wrote 10 sizes")
