import ClipSendCore
import UIKit
import UniformTypeIdentifiers
import Network
import Security
import CryptoKit
import Darwin
import ImageIO

private struct ShareComputer: Decodable, Identifiable {
    let computerID: String; let name: String; let certificateSHA256: String; let deviceID: String; let secret: String
    var id: String { computerID }
}
private struct ShareFile { let url: URL; let name: String; let mime: String; let size: Int64; let sha256: String }

@objc(ClipSendShareShareViewController) @MainActor final class ShareViewController: UIViewController {
    private let titleLabel = UILabel(); private let stack = UIStackView(); private let progress = UIProgressView(progressViewStyle: .default)
    private var text = ""; private var files: [ShareFile] = []; private var computers: [ShareComputer] = []
    private var browser: ComputerBrowser?; private var deviceButtons: [String: UIButton] = [:]; private var discoveryTimeout: Task<Void, Never>?; private var endpoints: [String: URL] = [:]; private var task: Task<Void, Never>?; private var prepareTask: Task<Void, Never>?
    private static let ink = UIColor { $0.userInterfaceStyle == .dark
        ? UIColor(red: 0.60, green: 0.84, blue: 0.72, alpha: 1)
        : UIColor(red: 0.07, green: 0.35, blue: 0.32, alpha: 1) }
    private static let canvas = UIColor { $0.userInterfaceStyle == .dark
        ? UIColor(red: 0.055, green: 0.075, blue: 0.073, alpha: 1)
        : UIColor(red: 0.95, green: 0.96, blue: 0.94, alpha: 1) }
    private var progressCount = 0
    private let detailLabel = UILabel()
    private let summaryStack = UIStackView()
    private let cancelButton = UIButton(type: .system)
    private let retryButton = UIButton(type: .system)
    private let spinner = UIActivityIndicatorView(style: .medium)
    private var sending = false
    private var finished = false
    private var sendFailed = false
    private var cancelling = false
    private lazy var shareDirectory: URL = {
        let temp = FileManager.default.temporaryDirectory
        if let old = try? FileManager.default.contentsOfDirectory(at: temp, includingPropertiesForKeys: nil) { old.filter { $0.lastPathComponent.hasPrefix("ClipSend-Share-") }.forEach { try? FileManager.default.removeItem(at: $0) } }
        let dir = temp.appendingPathComponent("ClipSend-Share-" + UUID().uuidString, isDirectory: true)
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700]); return dir
    }()

    override func viewDidLoad() {
        super.viewDidLoad()
        view.backgroundColor = Self.canvas
        view.tintColor = Self.ink
        titleLabel.font = .preferredFont(forTextStyle: .headline)
        titleLabel.text = "Preparing share…"
        titleLabel.accessibilityTraits = .header
        for label in [titleLabel, detailLabel] { label.numberOfLines = 0; label.adjustsFontForContentSizeCategory = true }
        detailLabel.font = .preferredFont(forTextStyle: .subheadline)
        detailLabel.textColor = .secondaryLabel
        detailLabel.text = "Choose a computer to send this item."
        stack.axis = .vertical; stack.spacing = 10
        summaryStack.axis = .vertical; summaryStack.spacing = 16
        summaryStack.isLayoutMarginsRelativeArrangement = true
        summaryStack.directionalLayoutMargins = NSDirectionalEdgeInsets(top: 20, leading: 20, bottom: 20, trailing: 20)
        summaryStack.backgroundColor = .secondarySystemGroupedBackground
        summaryStack.layer.cornerRadius = 20
        summaryStack.isHidden = true
        progress.isHidden = true
        spinner.startAnimating()
        configure(cancelButton, title: "Cancel", prominent: false)
        cancelButton.addTarget(self, action: #selector(cancelShare), for: .touchUpInside)
        configure(retryButton, title: "Check again", prominent: false)
        retryButton.isHidden = true
        retryButton.addAction(UIAction { [weak self] _ in self?.restartDiscovery() }, for: .touchUpInside)
        let brand = UILabel()
        brand.text = "ClipSend"; brand.font = .preferredFont(forTextStyle: .headline)
        brand.adjustsFontForContentSizeCategory = true
        let mark = UIImageView(image: UIImage(systemName: "list.clipboard.fill"))
        mark.preferredSymbolConfiguration = UIImage.SymbolConfiguration(pointSize: 23, weight: .medium)
        mark.tintColor = UIColor(red: 0.96, green: 0.96, blue: 0.91, alpha: 1)
        mark.backgroundColor = UIColor(red: 0.07, green: 0.35, blue: 0.32, alpha: 1)
        mark.contentMode = .center; mark.layer.cornerRadius = 10; mark.isAccessibilityElement = false
        NSLayoutConstraint.activate([mark.widthAnchor.constraint(equalToConstant: 38), mark.heightAnchor.constraint(equalToConstant: 38)])
        let arrow = UIImageView(image: UIImage(systemName: "arrow.up.right.circle.fill"))
        arrow.preferredSymbolConfiguration = UIImage.SymbolConfiguration(paletteColors: [UIColor(red: 0.60, green: 0.84, blue: 0.72, alpha: 1), UIColor(red: 0.07, green: 0.35, blue: 0.32, alpha: 1)])
        arrow.frame = CGRect(x: 19, y: 19, width: 19, height: 19)
        mark.addSubview(arrow)
        let header = UIStackView(arrangedSubviews: [mark, brand, UIView(), cancelButton])
        header.alignment = .center; header.spacing = 11
        cancelButton.setContentHuggingPriority(.required, for: .horizontal)
        let state = UIStackView(arrangedSubviews: [titleLabel, detailLabel])
        state.axis = .vertical; state.spacing = 6
        let scroll = UIScrollView()
        let content = UIStackView(arrangedSubviews: [header, summaryStack, state, spinner, progress, stack, retryButton])
        content.axis = .vertical; content.spacing = 20; content.translatesAutoresizingMaskIntoConstraints = false
        scroll.addSubview(content); view.addSubview(scroll); scroll.translatesAutoresizingMaskIntoConstraints = false
        NSLayoutConstraint.activate([
            scroll.leadingAnchor.constraint(equalTo: view.safeAreaLayoutGuide.leadingAnchor),
            scroll.trailingAnchor.constraint(equalTo: view.safeAreaLayoutGuide.trailingAnchor),
            scroll.topAnchor.constraint(equalTo: view.safeAreaLayoutGuide.topAnchor),
            scroll.bottomAnchor.constraint(equalTo: view.safeAreaLayoutGuide.bottomAnchor),
            content.centerXAnchor.constraint(equalTo: scroll.frameLayoutGuide.centerXAnchor),
            content.widthAnchor.constraint(lessThanOrEqualToConstant: 600),
            content.leadingAnchor.constraint(greaterThanOrEqualTo: scroll.contentLayoutGuide.leadingAnchor, constant: 20),
            content.trailingAnchor.constraint(lessThanOrEqualTo: scroll.contentLayoutGuide.trailingAnchor, constant: -20),
            content.topAnchor.constraint(equalTo: scroll.contentLayoutGuide.topAnchor, constant: 24),
            content.bottomAnchor.constraint(equalTo: scroll.contentLayoutGuide.bottomAnchor, constant: -20),
            scroll.contentLayoutGuide.widthAnchor.constraint(equalTo: scroll.frameLayoutGuide.widthAnchor)
        ])
        let fill = content.widthAnchor.constraint(equalTo: scroll.frameLayoutGuide.widthAnchor, constant: -40)
        fill.priority = .defaultHigh; fill.isActive = true
        prepareTask = Task { await prepare(); prepareTask = nil }
    }

    private func configure(_ button: UIButton, title: String, prominent: Bool) {
        var config = prominent ? UIButton.Configuration.filled() : UIButton.Configuration.plain()
        config.title = title
        config.baseBackgroundColor = Self.ink
        config.baseForegroundColor = prominent ? .secondarySystemGroupedBackground : Self.ink
        config.background.cornerRadius = 12
        config.cornerStyle = .fixed
        if prominent {
            config.image = UIImage(systemName: "desktopcomputer")
            config.imagePadding = 12
            config.titleAlignment = .leading
        }
        config.contentInsets = NSDirectionalEdgeInsets(top: 12, leading: 16, bottom: 12, trailing: 16)
        button.configuration = config
        button.titleLabel?.numberOfLines = 0
        button.titleLabel?.adjustsFontForContentSizeCategory = true
        button.heightAnchor.constraint(greaterThanOrEqualToConstant: 44).isActive = true
    }

    private func showSummary() {
        summaryStack.isHidden = false
        if !text.isEmpty {
            let preview = UILabel()
            preview.text = text; preview.numberOfLines = 4
            preview.font = .preferredFont(forTextStyle: .body); preview.adjustsFontForContentSizeCategory = true
            summaryStack.addArrangedSubview(preview)
        }
        for file in files {
            if !summaryStack.arrangedSubviews.isEmpty {
                let divider = UIView(); divider.backgroundColor = .separator
                divider.heightAnchor.constraint(equalToConstant: 0.5).isActive = true
                summaryStack.addArrangedSubview(divider)
            }
            let icon = UIImageView(image: UIImage(systemName: file.mime.hasPrefix("image/") ? "photo" : "doc.text"))
            icon.tintColor = Self.ink; icon.contentMode = .center
            icon.backgroundColor = Self.ink.withAlphaComponent(0.06)
            icon.layer.cornerRadius = 10; icon.clipsToBounds = true; icon.isAccessibilityElement = false
            icon.preferredSymbolConfiguration = UIImage.SymbolConfiguration(pointSize: 22)
            NSLayoutConstraint.activate([icon.widthAnchor.constraint(equalToConstant: 52), icon.heightAnchor.constraint(equalToConstant: 52)])
            let name = UILabel(); name.text = file.name; name.numberOfLines = 0
            name.font = .preferredFont(forTextStyle: .subheadline); name.adjustsFontForContentSizeCategory = true
            let size = UILabel(); size.text = ByteCountFormatter.string(fromByteCount: file.size, countStyle: .file)
            size.font = .preferredFont(forTextStyle: .caption1); size.textColor = .secondaryLabel
            size.adjustsFontForContentSizeCategory = true
            let labels = UIStackView(arrangedSubviews: [name, size]); labels.axis = .vertical; labels.spacing = 3
            let row = UIStackView(arrangedSubviews: [icon, labels]); row.alignment = .center; row.spacing = 12
            summaryStack.addArrangedSubview(row)
            if file.mime.hasPrefix("image/") {
                let url = file.url, mime = file.mime
                Task { [weak icon] in
                    let image = await Task.detached(priority: .utility) { Self.thumbnail(url, mime: mime) }.value
                    if let image { icon?.contentMode = .scaleAspectFit; icon?.image = image }
                }
            }
        }
    }

    nonisolated private static func thumbnail(_ url: URL, mime: String) -> UIImage? {
        let type: UTType? = switch mime.lowercased() {
        case "image/png": .png
        case "image/jpeg", "image/jpg": .jpeg
        case "image/heic": .heic
        default: nil
        }
        guard let type, let source = CGImageSourceCreateWithURL(url as CFURL, [kCGImageSourceShouldCache: false] as CFDictionary),
              CGImageSourceGetType(source) as String? == type.identifier,
              let properties = CGImageSourceCopyPropertiesAtIndex(source, 0, nil) as? [CFString: Any],
              let width = properties[kCGImagePropertyPixelWidth] as? Int,
              let height = properties[kCGImagePropertyPixelHeight] as? Int, width > 0, height > 0,
              let image = CGImageSourceCreateThumbnailAtIndex(source, 0, [
                kCGImageSourceCreateThumbnailFromImageAlways: true,
                kCGImageSourceCreateThumbnailWithTransform: true,
                kCGImageSourceShouldCacheImmediately: true,
                kCGImageSourceThumbnailMaxPixelSize: 160
              ] as CFDictionary) else { return nil }
        return UIImage(cgImage: image)
    }

    private func prepare() async {
        guard let extensionItem = extensionContext?.inputItems.first as? NSExtensionItem else { finish("No shared content was provided."); return }
        let providers = extensionItem.attachments ?? []
        guard providers.count <= 16 else { finish("ClipSend supports up to 16 files at a time."); return }
        do {
            for provider in providers {
                try Task.checkCancellation()
                if provider.hasItemConformingToTypeIdentifier(UTType.fileURL.identifier) || provider.hasItemConformingToTypeIdentifier(UTType.image.identifier) {
                    let type = provider.hasItemConformingToTypeIdentifier(UTType.fileURL.identifier) ? UTType.fileURL.identifier : UTType.image.identifier
                    let file = try await copyProviderFile(provider, type: type)
                    let values = try file.resourceValues(forKeys: [.fileSizeKey, .contentTypeKey])
                    guard let size = values.fileSize, size <= 128 * 1024 * 1024 else { throw ShareError.limit }
                    let mime = values.contentType?.preferredMIMEType ?? "application/octet-stream"
                    files.append(ShareFile(url: file, name: file.lastPathComponent, mime: mime, size: Int64(size), sha256: try digest(file)))
                } else if provider.hasItemConformingToTypeIdentifier(UTType.plainText.identifier) {
                    if let value = try await provider.loadItem(forTypeIdentifier: UTType.plainText.identifier) as? String { text += value }
                } else if provider.hasItemConformingToTypeIdentifier(UTType.url.identifier) {
                    if let value = try await provider.loadItem(forTypeIdentifier: UTType.url.identifier) as? URL { text += value.absoluteString }
                    else if let value = try await provider.loadItem(forTypeIdentifier: UTType.url.identifier) as? NSURL { text += value.absoluteString ?? "" }
                } else if provider.hasItemConformingToTypeIdentifier(UTType.item.identifier) {
                    let file = try await copyProviderFile(provider, type: UTType.item.identifier)
                    let values = try file.resourceValues(forKeys: [.fileSizeKey, .contentTypeKey])
                    guard let size = values.fileSize, size <= 128 * 1024 * 1024 else { throw ShareError.limit }
                    let mime = values.contentType?.preferredMIMEType ?? "application/octet-stream"
                    files.append(ShareFile(url: file, name: file.lastPathComponent, mime: mime, size: Int64(size), sha256: try digest(file)))
                }
            }
            guard text.utf8.count <= 64_000, files.count <= 16, !text.isEmpty || !files.isEmpty,
                  files.reduce(Int64(0), { $0 + $1.size }) <= 256 * 1024 * 1024 else { throw ShareError.limit }
            try Task.checkCancellation()
            showSummary()
            computers = try loadPairings()
            guard !computers.isEmpty else { finish("No paired computers are available. Open ClipSend to pair a computer first."); return }
            showDeviceList(); discover()
        } catch is CancellationError { finish("Share cancelled.") }
        catch ShareError.keychain { finish("ClipSend’s signed Share extension cannot access the existing pairing Keychain group. Open ClipSend to send instead.") }
        catch { finish("Couldn’t prepare this share. Check the files and try again.") }
    }

    private func showDeviceList() {
        for computer in computers {
            let button = UIButton(type: .system)
            configure(button, title: "Send to \(computer.name)", prominent: true)
            button.isEnabled = false; deviceButtons[computer.id] = button
            button.addAction(UIAction { [weak self] _ in self?.beginSend(computer) }, for: .touchUpInside)
            stack.addArrangedSubview(button)
        }
        updateAvailability()
    }

    private func updateAvailability() {
        guard !finished, !sending else { return }
        for (id, button) in deviceButtons { button.isEnabled = endpoints[id] != nil }
        guard !sendFailed else { return }
        retryButton.isHidden = !endpoints.isEmpty
        titleLabel.text = endpoints.isEmpty ? "Looking for computers…" : "Send to computer"
        detailLabel.text = "Turn on Receive in ClipSend on your computer and use the same Wi-Fi."
        if endpoints.isEmpty { spinner.startAnimating() } else { spinner.stopAnimating() }
    }

    private func restartDiscovery() {
        guard !finished, !sending else { return }
        browser?.stop(); discoveryTimeout?.cancel(); endpoints.removeAll()
        retryButton.isHidden = true; sendFailed = false
        updateAvailability(); discover()
    }

    private func discover() {
        browser = ComputerBrowser(found: { [weak self] id, _, host, port, receiving in
            Task { @MainActor in
                guard let self, !self.finished, self.computers.contains(where: { $0.id == id }) else { return }
                if receiving, let host, let endpoint = URL(string: "https://\(host):\(port)") { self.endpoints[id] = endpoint }
                else { self.endpoints.removeValue(forKey: id) }
                self.updateAvailability()
            }
        }, removed: { [weak self] id, _ in
            Task { @MainActor in
                guard let self, !self.finished else { return }
                self.endpoints.removeValue(forKey: id); self.updateAvailability()
            }
        })
        browser?.start()
        discoveryTimeout = Task { [weak self] in
            try? await Task.sleep(for: .seconds(10))
            guard !Task.isCancelled, let self, !self.finished, !self.sending, self.endpoints.isEmpty else { return }
            self.spinner.stopAnimating()
            self.titleLabel.text = "No computer available"
            self.retryButton.isHidden = false
        }
    }

    private func beginSend(_ computer: ShareComputer) {
        guard !sending, !finished, let endpoint = endpoints[computer.computerID] else { return }
        sending = true; cancelling = false; sendFailed = false
        discoveryTimeout?.cancel()
        deviceButtons.values.forEach { $0.isEnabled = false }
        retryButton.isHidden = true
        spinner.startAnimating(); progress.progress = 0; progress.isHidden = files.isEmpty
        titleLabel.text = "Sending to \(computer.name)…"
        detailLabel.text = "Keep ClipSend open until the send finishes."
        task = Task { [weak self] in
            guard let self else { return }
            do {
                try await upload(computer, endpoint: endpoint)
                finish("Received by \(computer.name).")
            } catch {
                if cancelling || error is CancellationError { finish("Share cancelled.") }
                else {
                    // Keep staged content for an explicit retry; final dismissal still cleans it up.
                    sending = false; sendFailed = true; progress.isHidden = true; spinner.stopAnimating()
                    deviceButtons.forEach { id, button in button.isEnabled = endpoints[id] != nil }
                    titleLabel.text = "Couldn’t confirm delivery"
                    detailLabel.text = "Check Wi-Fi and Receive on \(computer.name). Retrying may send the item again."
                    retryButton.isHidden = false
                }
            }
            task = nil
        }
    }

    private func upload(_ computer: ShareComputer, endpoint: URL) async throws {
        let client = PinnedSession(fingerprint: computer.certificateSHA256)
        defer { client.session.invalidateAndCancel() }
        let items = files.map { ["id": UUID().uuidString.replacingOccurrences(of: "-", with: ""), "name": $0.name, "mime": $0.mime, "size": "\($0.size)", "sha256": $0.sha256] }
        // Encode integer sizes using the protocol's numeric JSON representation.
        let attachmentValues: [[String: Any]] = files.enumerated().map { index, file in ["id": items[index]["id"]!, "name": file.name, "mime": file.mime, "size": file.size, "sha256": file.sha256] }
        let body: [String: Any] = ["version": 1, "text": text, "attachments": attachmentValues]
        var create = URLRequest(url: endpoint.appendingPathComponent("v1/incoming"), timeoutInterval: 30); create.httpMethod = "POST"; authorize(&create, computer); create.setValue("application/json", forHTTPHeaderField: "Content-Type"); create.httpBody = try JSONSerialization.data(withJSONObject: body)
        let (data, response) = try await client.session.data(for: create, delegate: client)
        guard (response as? HTTPURLResponse)?.statusCode == 201, let json = try JSONSerialization.jsonObject(with: data) as? [String: Any], let transferID = json["id"] as? String, transferID.range(of: "^[A-Za-z0-9_-]+$", options: .regularExpression) != nil else { throw ShareError.network }
        do {
            for (index, file) in files.enumerated() {
                try Task.checkCancellation(); let itemID = items[index]["id"]!
                var put = URLRequest(url: endpoint.appendingPathComponent("v1/incoming/\(transferID)/items/\(itemID)"), timeoutInterval: 120); put.httpMethod = "PUT"; authorize(&put, computer); put.setValue("application/octet-stream", forHTTPHeaderField: "Content-Type")
                let (_, reply) = try await client.session.upload(for: put, fromFile: file.url, delegate: client)
                guard (reply as? HTTPURLResponse)?.statusCode == 204 else { throw ShareError.network }
                await MainActor.run { progressCount = index + 1; progress.progress = Float(index + 1) / Float(max(1, files.count)); titleLabel.text = "Sending file \(index + 1) of \(files.count)…" }
            }
            try Task.checkCancellation(); var complete = URLRequest(url: endpoint.appendingPathComponent("v1/incoming/\(transferID)/complete"), timeoutInterval: 30); complete.httpMethod = "POST"; authorize(&complete, computer)
            let (_, done) = try await client.session.data(for: complete, delegate: client); guard (done as? HTTPURLResponse)?.statusCode == 204 else { throw ShareError.network }
        } catch {
            var cancel = URLRequest(url: endpoint.appendingPathComponent("v1/incoming/\(transferID)"), timeoutInterval: 10); cancel.httpMethod = "DELETE"; authorize(&cancel, computer); _ = try? await client.session.data(for: cancel, delegate: client); throw error
        }
    }

    private func authorize(_ request: inout URLRequest, _ computer: ShareComputer) { request.setValue("Bearer \(computer.secret)", forHTTPHeaderField: "Authorization") }
    private func loadPairings() throws -> [ShareComputer] {
        let extensionBundle = Bundle.main.bundleIdentifier ?? ""
        guard extensionBundle.hasSuffix(".share") else { throw ShareError.keychain }
        let rootBundle = String(extensionBundle.dropLast(".share".count))
        let probeService = "ClipSend-group-probe-" + UUID().uuidString
        let probeAccount = UUID().uuidString
        let probe: [String: Any] = [kSecClass as String: kSecClassGenericPassword, kSecAttrService as String: probeService, kSecAttrAccount as String: probeAccount, kSecValueData as String: Data(UUID().uuidString.utf8), kSecReturnAttributes as String: true]
        var probeResult: CFTypeRef?
        let probeStatus = SecItemAdd(probe as CFDictionary, &probeResult)
        defer { let cleanup: [String: Any] = [kSecClass as String: kSecClassGenericPassword, kSecAttrService as String: probeService, kSecAttrAccount as String: probeAccount]; SecItemDelete(cleanup as CFDictionary) }
        guard probeStatus == errSecSuccess, let attrs = probeResult as? [String: Any], let ownGroup = attrs[kSecAttrAccessGroup as String] as? String else { throw ShareError.keychain }
        let extensionSuffix = ".share"
        let rootGroup = ownGroup.hasSuffix(extensionSuffix) ? String(ownGroup.dropLast(extensionSuffix.count)) : ""
        let groups = [rootGroup, ownGroup].filter { !$0.isEmpty }
        for group in groups {
            let query: [String: Any] = [kSecClass as String: kSecClassGenericPassword, kSecAttrService as String: rootBundle + ".paired-computers", kSecAttrAccount as String: "computers", kSecAttrAccessGroup as String: group, kSecReturnData as String: true, kSecMatchLimit as String: kSecMatchLimitOne]
            var output: CFTypeRef?
            if SecItemCopyMatching(query as CFDictionary, &output) == errSecSuccess, let data = output as? Data { return try JSONDecoder().decode([ShareComputer].self, from: data) }
        }
        throw ShareError.keychain
    }
    private func digest(_ url: URL) throws -> String { let h = try FileHandle(forReadingFrom: url); defer { try? h.close() }; var sha = SHA256(); while let part = try h.read(upToCount: 1024 * 1024), !part.isEmpty { sha.update(data: part) }; return sha.finalize().map { String(format: "%02x", $0) }.joined() }
    private func copyProviderFile(_ provider: NSItemProvider, type: String) async throws -> URL {
        let directory = shareDirectory
        let suggestedName = Self.safeFilename(provider.suggestedName ?? "shared-file")
        let fileCount = files.count
        let aggregateBytes = files.reduce(Int64(0)) { $0 + $1.size }
        guard fileCount < 16, aggregateBytes <= 256 * 1024 * 1024 else { throw ShareError.limit }
        if type == UTType.fileURL.identifier {
            // public.file-url describes a reference, not the file's bytes.
            // loadFileRepresentation for this UTI can return a serialized URL plist.
            guard let url = try await provider.loadItem(forTypeIdentifier: type) as? URL, url.isFileURL else { throw ShareError.network }
            let scoped = url.startAccessingSecurityScopedResource()
            defer { if scoped { url.stopAccessingSecurityScopedResource() } }
            return try Self.stageURL(url, directory: directory, name: url.lastPathComponent, aggregateBytes: aggregateBytes)
        }
        return try await withCheckedThrowingContinuation { (continuation: CheckedContinuation<URL, Error>) in
            provider.loadFileRepresentation(forTypeIdentifier: type) { url, error in
                if let error { continuation.resume(throwing: error); return }
                guard let url else { continuation.resume(throwing: ShareError.network); return }
                do { continuation.resume(returning: try Self.stageURL(url, directory: directory, name: suggestedName, aggregateBytes: aggregateBytes)) }
                catch { continuation.resume(throwing: error) }
            }
        }
    }
    nonisolated private static func stageURL(_ url: URL, directory: URL, name suggestedName: String, aggregateBytes: Int64) throws -> URL {
        guard url.isFileURL, let size = try url.resourceValues(forKeys: [.fileSizeKey]).fileSize,
              Int64(size) <= min(Int64(128 * 1024 * 1024), Int64(256 * 1024 * 1024) - aggregateBytes) else { throw ShareError.limit }
        let itemDirectory = directory.appendingPathComponent(UUID().uuidString, isDirectory: true)
        try FileManager.default.createDirectory(at: itemDirectory, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        let name = URL(fileURLWithPath: suggestedName).pathExtension.isEmpty && !url.pathExtension.isEmpty ? suggestedName + "." + url.pathExtension : suggestedName
        let dest = itemDirectory.appendingPathComponent(safeFilename(name))
        do {
            try FileManager.default.copyItem(at: url, to: dest)
            guard let copied = try dest.resourceValues(forKeys: [.fileSizeKey]).fileSize, copied == size else { throw ShareError.limit }
            return dest
        } catch { try? FileManager.default.removeItem(at: itemDirectory); throw error }
    }
    nonisolated private static func safeFilename(_ input: String) -> String {
        let leaf = (input.replacingOccurrences(of: "\\", with: "/") as NSString).lastPathComponent
        var result = ""
        for scalar in leaf.unicodeScalars where !CharacterSet.controlCharacters.contains(scalar) && scalar != "/" && scalar != "\\" && scalar != ":" {
            guard (result + String(scalar)).utf8.count <= 255 else { break }; result.unicodeScalars.append(scalar)
        }
        let value = result.trimmingCharacters(in: .whitespacesAndNewlines)
        return value.isEmpty || value == "." || value == ".." ? "shared-file" : value
    }
    @objc private func cancelShare() {
        cancelling = true; cancelButton.isEnabled = false
        if let prepareTask { titleLabel.text = "Cancelling…"; prepareTask.cancel(); return }
        if let task { titleLabel.text = "Cancelling…"; task.cancel(); return }
        finish("Share cancelled.")
    }
    private func finish(_ message: String) {
        guard !finished else { return }
        finished = true; sending = false
        titleLabel.text = message; detailLabel.isHidden = true
        stack.isHidden = true; retryButton.isHidden = true; cancelButton.isHidden = true
        progress.isHidden = true; spinner.stopAnimating()
        browser?.stop(); discoveryTimeout?.cancel()
        try? FileManager.default.removeItem(at: shareDirectory)
        if message.hasPrefix("Received by") { extensionContext?.completeRequest(returningItems: nil) }
        else {
            let done = UIButton(type: .system)
            configure(done, title: "Done", prominent: false)
            done.addAction(UIAction { [weak self] _ in self?.extensionContext?.cancelRequest(withError: ShareError.cancelled) }, for: .touchUpInside)
            stack.arrangedSubviews.forEach { $0.removeFromSuperview() }
            stack.addArrangedSubview(done); stack.isHidden = false
            UIAccessibility.post(notification: .screenChanged, argument: titleLabel)
        }
    }
    private enum ShareError: Error { case keychain, network, limit, cancelled }
}

private final class PinnedSession: NSObject, URLSessionDelegate, URLSessionTaskDelegate, @unchecked Sendable {
    let fingerprint: String; lazy var session = URLSession(configuration: .ephemeral, delegate: self, delegateQueue: nil)
    init(fingerprint: String) { self.fingerprint = fingerprint }
    func urlSession(_ session: URLSession, didReceive challenge: URLAuthenticationChallenge, completionHandler: @escaping (URLSession.AuthChallengeDisposition, URLCredential?) -> Void) {
        guard challenge.protectionSpace.authenticationMethod == NSURLAuthenticationMethodServerTrust, let trust = challenge.protectionSpace.serverTrust, let cert = (SecTrustCopyCertificateChain(trust) as? [SecCertificate])?.first else { completionHandler(.cancelAuthenticationChallenge, nil); return }
        let hash = SHA256.hash(data: SecCertificateCopyData(cert) as Data).map { String(format: "%02x", $0) }.joined()
        guard hash == fingerprint, SecTrustSetAnchorCertificates(trust, [cert] as CFArray) == errSecSuccess, SecTrustSetAnchorCertificatesOnly(trust, true) == errSecSuccess, SecTrustSetPolicies(trust, SecPolicyCreateBasicX509()) == errSecSuccess, SecTrustEvaluateWithError(trust, nil) else { completionHandler(.cancelAuthenticationChallenge, nil); return }
        completionHandler(.useCredential, URLCredential(trust: trust))
    }
    func urlSession(_ session: URLSession, task: URLSessionTask, willPerformHTTPRedirection response: HTTPURLResponse, newRequest request: URLRequest, completionHandler: @escaping (URLRequest?) -> Void) { completionHandler(nil) }
}
