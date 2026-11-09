import UIKit
import UniformTypeIdentifiers

struct ClipboardSendPayload {
    let text: String
    let files: [URL]
    let directory: URL?
    func removeTemporaryFiles() { if let directory { try? FileManager.default.removeItem(at: directory) } }
}

enum ClipboardSendError: LocalizedError {
    case empty, unsupported, limit, unreadable
    var errorDescription: String? {
        switch self {
        case .empty: "The clipboard is empty."
        case .unsupported: "This clipboard content can’t be sent. Try sharing it to ClipSend."
        case .limit: "Clipboard content exceeds the transfer limits."
        case .unreadable: "Couldn’t read the clipboard content. Try copying it again."
        }
    }
}

@MainActor enum ClipboardSend {
    // Called only by the explicit Paste & Send action. One representation per item.
    static func read() async throws -> ClipboardSendPayload {
        let clipboard = UIPasteboard.general
        // UIKit owns text decoding, including its private item-provider representations.
        // Keep image/file items on the representation path so mixed payloads aren't dropped.
        if !clipboard.hasImages, !(clipboard.urls ?? []).contains(where: \.isFileURL),
           let strings = clipboard.strings, !strings.isEmpty {
            let text = strings.joined(separator: "\n")
            guard !text.isEmpty else { throw ClipboardSendError.empty }
            guard text.utf8.count <= 64_000 else { throw ClipboardSendError.limit }
            return ClipboardSendPayload(text: text, files: [], directory: nil)
        }
        let providers = clipboard.itemProviders
        guard !providers.isEmpty else { throw ClipboardSendError.empty }
        guard providers.count <= 16 else { throw ClipboardSendError.limit }
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("ClipSend-Clipboard-" + UUID().uuidString, isDirectory: true)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        var text: [String] = [], files: [URL] = []
        var bytes: Int64 = 0
        do {
            for provider in providers {
                // Prefer a lossless, broadly copyable clipboard representation when offered.
                let preferredImage = [UTType.png.identifier, UTType.jpeg.identifier].first {
                    provider.hasItemConformingToTypeIdentifier($0)
                }
                let fileType = preferredImage ?? provider.registeredTypeIdentifiers.first { type in
                    guard let value = UTType(type) else { return false }
                    return value.conforms(to: .image) || value.conforms(to: .pdf) || value.conforms(to: .movie) || value.conforms(to: .audio)
                }
                if provider.hasItemConformingToTypeIdentifier(UTType.fileURL.identifier) {
                    guard let url = try await provider.loadItem(forTypeIdentifier: UTType.fileURL.identifier) as? URL, url.isFileURL else { throw ClipboardSendError.unreadable }
                    let scoped = url.startAccessingSecurityScopedResource()
                    defer { if scoped { url.stopAccessingSecurityScopedResource() } }
                    files.append(try stage(url, in: directory, name: url.lastPathComponent, used: bytes))
                } else if let type = fileType {
                    let used = bytes
                    let name = provider.suggestedName ?? "Clipboard"
                    let url: URL = try await withCheckedThrowingContinuation { continuation in
                        provider.loadFileRepresentation(forTypeIdentifier: type) { url, error in
                            do {
                                if let error { throw error }
                                guard let url else { throw ClipboardSendError.unreadable }
                                continuation.resume(returning: try stage(url, in: directory, name: name, used: used))
                            } catch { continuation.resume(throwing: error) }
                        }
                    }
                    files.append(url)
                } else if provider.hasItemConformingToTypeIdentifier(UTType.plainText.identifier) {
                    let type = provider.registeredTypeIdentifiers.first { UTType($0)?.conforms(to: .plainText) == true } ?? UTType.plainText.identifier
                    let value = try await provider.loadItem(forTypeIdentifier: type)
                    if let string = value as? String { text.append(string) }
                    else if let data = value as? Data {
                        guard data.count <= 256_000 else { throw ClipboardSendError.limit }
                        let encoding: String.Encoding = type == UTType.utf16PlainText.identifier ? .utf16 : .utf8
                        guard let string = String(data: data, encoding: encoding) else { throw ClipboardSendError.unreadable }
                        text.append(string)
                    } else { throw ClipboardSendError.unreadable }
                } else if provider.hasItemConformingToTypeIdentifier(UTType.url.identifier),
                          let url = try await provider.loadItem(forTypeIdentifier: UTType.url.identifier) as? URL {
                    text.append(url.absoluteString)
                } else { throw ClipboardSendError.unsupported }
                bytes = try files.reduce(0) { try $0 + Int64($1.resourceValues(forKeys: [.fileSizeKey]).fileSize ?? 0) }
                guard text.joined(separator: "\n").utf8.count <= 64_000 else { throw ClipboardSendError.limit }
            }
            let value = text.joined(separator: "\n")
            guard !value.isEmpty || !files.isEmpty else { throw ClipboardSendError.empty }
            return ClipboardSendPayload(text: value, files: files, directory: directory)
        } catch {
            try? FileManager.default.removeItem(at: directory)
            throw error
        }
    }
    nonisolated private static func stage(_ source: URL, in directory: URL, name: String, used: Int64) throws -> URL {
        let properties = try source.resourceValues(forKeys: [.fileSizeKey, .isRegularFileKey])
        guard properties.isRegularFile == true, let size = properties.fileSize, size >= 0,
              size <= 128 * 1024 * 1024, used + Int64(size) <= 256 * 1024 * 1024 else { throw ClipboardSendError.limit }
        let folder = directory.appendingPathComponent(UUID().uuidString, isDirectory: true)
        try FileManager.default.createDirectory(at: folder, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        let suffix = URL(fileURLWithPath: name).pathExtension.isEmpty && !source.pathExtension.isEmpty ? "." + source.pathExtension : ""
        let target = folder.appendingPathComponent(SafeFilename.sanitize(name + suffix))
        try FileManager.default.copyItem(at: source, to: target)
        guard try target.resourceValues(forKeys: [.fileSizeKey]).fileSize == size else { throw ClipboardSendError.unreadable }
        return target
    }
}
