import SwiftUI
import UIKit
import ImageIO
import UniformTypeIdentifiers

// The app icon's ink and mint, adapted for native light and dark surfaces.
enum ClipSendStyle {
    static let ink = Color(uiColor: UIColor { $0.userInterfaceStyle == .dark
        ? UIColor(red: 0.60, green: 0.84, blue: 0.72, alpha: 1)
        : UIColor(red: 0.07, green: 0.35, blue: 0.32, alpha: 1) })
    static let canvas = Color(uiColor: UIColor { $0.userInterfaceStyle == .dark
        ? UIColor(red: 0.055, green: 0.075, blue: 0.073, alpha: 1)
        : UIColor(red: 0.95, green: 0.96, blue: 0.94, alpha: 1) })
    static let surface = Color(uiColor: .secondarySystemGroupedBackground)
    static let wash = ink.opacity(0.08)
}

struct ClipSendMark: View {
    var body: some View {
        ZStack {
            RoundedRectangle(cornerRadius: 10).fill(Color(red: 0.07, green: 0.35, blue: 0.32))
            Image(systemName: "list.clipboard.fill")
                .font(.system(size: 23, weight: .medium))
                .foregroundStyle(Color(red: 0.96, green: 0.96, blue: 0.91))
                .offset(x: -2, y: 1)
            Image(systemName: "arrow.up.right.circle.fill")
                .font(.system(size: 19, weight: .bold))
                .foregroundStyle(Color(red: 0.60, green: 0.84, blue: 0.72), Color(red: 0.07, green: 0.35, blue: 0.32))
                .offset(x: 9, y: 9)
        }.frame(width: 38, height: 38).accessibilityHidden(true)
    }
}

struct ClipSendActionStyle: ButtonStyle {
    var prominent = false
    @Environment(\.isEnabled) private var isEnabled
    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .font(.subheadline.weight(.semibold))
            .padding(.horizontal, 14).frame(minHeight: 44)
            .foregroundStyle(prominent ? ClipSendStyle.surface : ClipSendStyle.ink)
            .background(prominent ? ClipSendStyle.ink : ClipSendStyle.wash, in: RoundedRectangle(cornerRadius: 12))
            .opacity(isEnabled ? (configuration.isPressed ? 0.65 : 1) : 0.35)
    }
}

// Only decode a bounded thumbnail, off the main actor. Never load original image bytes into a row.
enum ClipSendImagePreview {
    static func load(_ url: URL, mime: String?, maxPixelSize: Int) -> UIImage? {
        guard let source = CGImageSourceCreateWithURL(url as CFURL, [kCGImageSourceShouldCache: false] as CFDictionary),
              let sourceType = CGImageSourceGetType(source) as String?,
              [UTType.png.identifier, UTType.jpeg.identifier, UTType.heic.identifier].contains(sourceType) else { return nil }
        if let mime {
            let expected: UTType? = switch mime.lowercased() {
            case "image/png": .png
            case "image/jpeg", "image/jpg": .jpeg
            case "image/heic": .heic
            default: nil
            }
            guard expected?.identifier == sourceType else { return nil }
        }
        guard let properties = CGImageSourceCopyPropertiesAtIndex(source, 0, nil) as? [CFString: Any],
              let width = properties[kCGImagePropertyPixelWidth] as? Int,
              let height = properties[kCGImagePropertyPixelHeight] as? Int, width > 0, height > 0,
              let image = CGImageSourceCreateThumbnailAtIndex(source, 0, [
                kCGImageSourceCreateThumbnailFromImageAlways: true,
                kCGImageSourceCreateThumbnailWithTransform: true,
                kCGImageSourceShouldCacheImmediately: true,
                kCGImageSourceThumbnailMaxPixelSize: maxPixelSize
              ] as CFDictionary) else { return nil }
        return UIImage(cgImage: image)
    }
}

struct ClipSendThumbnail: View {
    let url: URL
    var mime: String? = nil
    var compact = false
    @State private var image: UIImage?
    var body: some View {
        Group {
            if let image {
                Image(uiImage: image).resizable().scaledToFit()
                    .frame(maxWidth: .infinity, maxHeight: compact ? 52 : 210)
            } else {
                Image(systemName: mime?.hasPrefix("image/") == true ? "photo" : "doc.text")
                    .font(compact ? .title3 : .largeTitle).foregroundStyle(ClipSendStyle.ink)
                    .frame(maxWidth: .infinity).frame(height: compact ? 52 : 100)
            }
        }
        .frame(width: compact ? 52 : nil)
        .padding(compact ? 0 : 12)
        .background(ClipSendStyle.wash, in: RoundedRectangle(cornerRadius: compact ? 10 : 12))
        .accessibilityHidden(true)
        .task(id: url) {
            let url = url, mime = mime, pixels = compact ? 160 : 720
            let result = await Task.detached(priority: .utility) {
                ClipSendImagePreview.load(url, mime: mime, maxPixelSize: pixels)
            }.value
            guard !Task.isCancelled else { return }
            image = result
        }
    }
}
