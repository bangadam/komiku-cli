import CoreGraphics
import CoreText
import Foundation
import ImageIO

struct Section {
    let output: String
    let lines: [String]
}

let arguments = CommandLine.arguments
if arguments.count != 3 {
    FileHandle.standardError.write(Data("usage: swift test/e2e/render.swift <transcript> <output-dir>\n".utf8))
    exit(2)
}

let transcriptURL = URL(fileURLWithPath: arguments[1])
let outputDirectory = URL(fileURLWithPath: arguments[2], isDirectory: true)
let text = try String(contentsOf: transcriptURL, encoding: .utf8)
let lines = text.split(separator: "\n", omittingEmptySubsequences: false).map(String.init)
var order: [String] = []
var grouped: [String: [String]] = [:]
var current: String?
for line in lines {
    if line.hasPrefix("[[render:"), line.hasSuffix("]]"), current == nil {
        let start = line.index(line.startIndex, offsetBy: 9)
        let end = line.index(line.endIndex, offsetBy: -2)
        let output = String(line[start..<end])
        guard output.hasSuffix(".png"), !output.contains("/"), !output.contains("\\") else {
            throw NSError(domain: "render", code: 1, userInfo: [NSLocalizedDescriptionKey: "invalid render output \(output)"])
        }
        if grouped[output] == nil { order.append(output) }
        current = output
    } else if line == "[[/render]]" {
        guard current != nil else {
            throw NSError(domain: "render", code: 2, userInfo: [NSLocalizedDescriptionKey: "unmatched render end marker"])
        }
        current = nil
    } else if let output = current {
        grouped[output, default: []].append(line)
    }
}
guard current == nil, !order.isEmpty else {
    throw NSError(domain: "render", code: 3, userInfo: [NSLocalizedDescriptionKey: "missing or unterminated render sections"])
}
let sections = order.map { Section(output: $0, lines: grouped[$0] ?? []) }

let font = CTFontCreateWithName("Menlo" as CFString, 16, nil)
let foreground = CGColor(red: 0.88, green: 0.91, blue: 0.94, alpha: 1)
let attributes: [NSAttributedString.Key: Any] = [
    NSAttributedString.Key(kCTFontAttributeName as String): font,
    NSAttributedString.Key(kCTForegroundColorAttributeName as String): foreground,
]
let lineHeight = ceil(CTFontGetAscent(font) + CTFontGetDescent(font) + CTFontGetLeading(font) + 5)
let padding: CGFloat = 28

func makeLine(_ text: String) -> CTLine {
    CTLineCreateWithAttributedString(NSAttributedString(string: text, attributes: attributes))
}

try FileManager.default.createDirectory(at: outputDirectory, withIntermediateDirectories: true)
for section in sections {
    let selected = section.lines
    guard !selected.isEmpty else {
        throw NSError(domain: "render", code: 4, userInfo: [NSLocalizedDescriptionKey: "empty render section \(section.output)"])
    }
    let textLines = selected.map(makeLine)
    let maxWidth = textLines.map { CGFloat(CTLineGetTypographicBounds($0, nil, nil, nil)) }.max() ?? 0
    let width = Int(ceil(maxWidth + padding * 2))
    let height = Int(ceil(CGFloat(selected.count) * lineHeight + padding * 2))
    guard let context = CGContext(
        data: nil,
        width: width,
        height: height,
        bitsPerComponent: 8,
        bytesPerRow: width * 4,
        space: CGColorSpaceCreateDeviceRGB(),
        bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue
    ) else {
        throw NSError(domain: "render", code: 2, userInfo: [NSLocalizedDescriptionKey: "cannot allocate bitmap"])
    }
    context.setFillColor(CGColor(red: 0.055, green: 0.067, blue: 0.082, alpha: 1))
    context.fill(CGRect(x: 0, y: 0, width: width, height: height))
    context.textMatrix = .identity
    for (index, line) in textLines.enumerated() {
        let baseline = CGFloat(height) - padding - CGFloat(index + 1) * lineHeight + CTFontGetDescent(font)
        context.textPosition = CGPoint(x: padding, y: baseline)
        CTLineDraw(line, context)
    }
    guard let image = context.makeImage() else {
        throw NSError(domain: "render", code: 3, userInfo: [NSLocalizedDescriptionKey: "cannot create image"])
    }
    let destinationURL = outputDirectory.appendingPathComponent(section.output) as CFURL
    guard let destination = CGImageDestinationCreateWithURL(destinationURL, "public.png" as CFString, 1, nil) else {
        throw NSError(domain: "render", code: 4, userInfo: [NSLocalizedDescriptionKey: "cannot create PNG destination"])
    }
    CGImageDestinationAddImage(destination, image, nil)
    guard CGImageDestinationFinalize(destination) else {
        throw NSError(domain: "render", code: 5, userInfo: [NSLocalizedDescriptionKey: "cannot encode PNG"])
    }
}
