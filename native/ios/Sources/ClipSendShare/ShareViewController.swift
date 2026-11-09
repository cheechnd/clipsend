import ClipSendCore
import UIKit
import UniformTypeIdentifiers
import Network
import Security
import CryptoKit
import Darwin

private struct ShareComputer: Decodable, Identifiable {
    let computerID: String; let name: String; let certificateSHA256: String; let deviceID: String; let secret: String
    var id: String { computerID }
}
private struct ShareFile { let url: URL; let name: String; let mime: String; let size: Int64; let sha256: String }

@objc(ClipSendShareShareViewController) @MainActor final class ShareViewController: UIViewController {
    private let titleLabel = UILabel(); private let stack = UIStackView(); private let progress = UIProgressView(progressViewStyle: .default)
    private var text = ""; private var files: [ShareFile] = []; private var computers: [ShareComputer] = []
    private var browser: ComputerBrowser?; private var deviceButtons: [String: UIButton] = [:]; private var discoveryTimeout: Task<Void, Never>?; private var endpoints: [String: URL] = [:]; private var task: Task<Void, Never>?; private var prepareTask: Task<Void, Never>?
    private var progressCount = 0
    private lazy var shareDirectory: URL = {
        let temp = FileManager.default.temporaryDirectory
        if let old = try? FileManager.default.contentsOfDirectory(at: temp, includingPropertiesForKeys: nil) { old.filter { $0.lastPathComponent.hasPrefix("ClipSend-Share-") }.forEach { try? FileManager.default.removeItem(at: $0) } }
        let dir = temp.appendingPathComponent("ClipSend-Share-" + UUID().uuidString, isDirectory: true)
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700]); return dir
    }()

    override func viewDidLoad() {
        super.viewDidLoad(); view.backgroundColor = .systemGroupedBackground
        titleLabel.font = .preferredFont(forTextStyle: .headline); titleLabel.text = "Preparing ClipSend…"
        stack.axis = .vertical; stack.spacing = 12
        let scroll = UIScrollView(); let content = UIStackView(arrangedSubviews: [titleLabel, progress, stack]); content.axis = .vertical; content.spacing = 16; content.translatesAutoresizingMaskIntoConstraints = false
        scroll.addSubview(content); view.addSubview(scroll); scroll.translatesAutoresizingMaskIntoConstraints = false
        NSLayoutConstraint.activate([scroll.leadingAnchor.constraint(equalTo: view.leadingAnchor, constant: 20), scroll.trailingAnchor.constraint(equalTo: view.trailingAnchor, constant: -20), scroll.topAnchor.constraint(equalTo: view.safeAreaLayoutGuide.topAnchor, constant: 20), scroll.bottomAnchor.constraint(equalTo: view.bottomAnchor, constant: -20), content.leadingAnchor.constraint(equalTo: scroll.contentLayoutGuide.leadingAnchor), content.trailingAnchor.constraint(equalTo: scroll.contentLayoutGuide.trailingAnchor), content.topAnchor.constraint(equalTo: scroll.contentLayoutGuide.topAnchor), content.bottomAnchor.constraint(equalTo: scroll.contentLayoutGuide.bottomAnchor), content.widthAnchor.constraint(equalTo: scroll.frameLayoutGuide.widthAnchor)])
        let cancel = UIButton(type: .system); cancel.setTitle("Cancel", for: .normal); cancel.contentHorizontalAlignment = .leading; cancel.heightAnchor.constraint(greaterThanOrEqualToConstant: 44).isActive = true; cancel.addTarget(self, action: #selector(cancelShare), for: .touchUpInside); stack.addArrangedSubview(cancel)
        prepareTask = Task { await prepare(); prepareTask = nil }
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
            computers = try loadPairings()
            guard !computers.isEmpty else { finish("No paired computers are available. Open ClipSend to pair a computer first."); return }
            showDeviceList(); discover()
        } catch is CancellationError { finish("Share cancelled.") }
        catch ShareError.keychain { finish("ClipSend’s signed Share extension cannot access the existing pairing Keychain group. Open ClipSend to send instead.") }
        catch { finish("Couldn’t prepare this share. Check the files and try again.") }
    }

    private func showDeviceList() {
        titleLabel.text = "Looking for receiving computers…"
        for computer in computers {
            let button = UIButton(type: .system); button.setTitle("Send to \(computer.name)", for: .normal); button.titleLabel?.font = .preferredFont(forTextStyle: .body); button.backgroundColor = .secondarySystemGroupedBackground; button.layer.cornerRadius = 12; button.heightAnchor.constraint(greaterThanOrEqualToConstant: 52).isActive = true
            button.isEnabled = false; deviceButtons[computer.id] = button
            button.addAction(UIAction { [weak self] _ in self?.beginSend(computer) }, for: .touchUpInside); stack.addArrangedSubview(button)
        }
        let note = UILabel(); note.text = "Enable Receive in the desktop ClipSend panel. Available computers become selectable."; note.textColor = .secondaryLabel; note.font = .preferredFont(forTextStyle: .footnote); stack.addArrangedSubview(note)
    }

    private func discover() {
        browser = ComputerBrowser(found: { [weak self] id, _, host, port, receiving in
            Task { @MainActor in
                guard let self, receiving, let host, self.computers.contains(where: { $0.id == id }),
                      let endpoint = URL(string: "https://\(host):\(port)") else { return }
                self.endpoints[id] = endpoint
                self.deviceButtons[id]?.isEnabled = true
                self.titleLabel.text = "Choose a receiving computer"
            }
        }, removed: { [weak self] id, _ in
            Task { @MainActor in self?.endpoints.removeValue(forKey: id); self?.deviceButtons[id]?.isEnabled = false }
        })
        browser?.start()
        discoveryTimeout = Task { [weak self] in
            try? await Task.sleep(for: .seconds(10))
            guard !Task.isCancelled, let self, self.endpoints.isEmpty else { return }
            self.titleLabel.text = "No receiving computer found. Check Wi-Fi and turn on Receive in the desktop panel."
        }
    }

    private func beginSend(_ computer: ShareComputer) {
        guard let endpoint = endpoints[computer.computerID] else { titleLabel.text = "Searching for \(computer.name)…"; return }
        task?.cancel(); progress.progress = 0
        task = Task { [weak self] in
            guard let self else { return }
            do { try await upload(computer, endpoint: endpoint); finish("Received by \(computer.name).") }
            catch is CancellationError { finish("Share cancelled. Try again when the computer is available.") }
            catch { finish("Couldn’t send to \(computer.name). Check its Receive setting and try again.") }
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
        if let prepareTask { titleLabel.text = "Cancelling…"; prepareTask.cancel(); return }
        if let task { titleLabel.text = "Cancelling…"; task.cancel(); return }
        finish("Share cancelled.")
    }
    private func finish(_ message: String) { titleLabel.text = message; stack.arrangedSubviews.filter { $0 is UIButton }.forEach { $0.isHidden = true }; progress.isHidden = true; browser?.stop(); discoveryTimeout?.cancel(); try? FileManager.default.removeItem(at: shareDirectory); if !message.hasPrefix("Received by") { let done = UIButton(type: .system); done.setTitle("Done", for: .normal); done.addAction(UIAction { [weak self] _ in self?.extensionContext?.cancelRequest(withError: ShareError.cancelled) }, for: .touchUpInside); stack.addArrangedSubview(done) }; if message.hasPrefix("Received by") { extensionContext?.completeRequest(returningItems: nil) } }
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
