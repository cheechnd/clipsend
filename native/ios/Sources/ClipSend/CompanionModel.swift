import ClipSendCore
import Foundation
import SwiftUI
import UIKit
import UserNotifications
import Security
#if canImport(CryptoKit)
import CryptoKit
#endif

@MainActor final class CompanionModel: ObservableObject {
    static let shared = CompanionModel()
    @Published private(set) var computers: [PairedComputer] = []
    @Published private(set) var messages: [ReceivedText] = []
    struct PendingClear: Identifiable {
        enum Content {
            case received(ReceivedText)
            case draft(text: String, files: [URL])
        }
        let id = UUID()
        let contents: [Content]
    }
    @Published private(set) var pendingClear: PendingClear?
    private var clearTask: Task<Void, Never>?
    var pendingDraftFiles: [URL] {
        (pendingClear?.contents ?? []).flatMap { content -> [URL] in
            if case .draft(_, let files) = content { return files }
            return []
        }
    }
    private var retainedMessages: [ReceivedText] {
        messages + (pendingClear?.contents ?? []).compactMap { content in
            if case .received(let message) = content { return message }
            return nil
        }
    }
    @Published var transferProgress: Double?
    @Published var transferStatus: String?
    @Published private(set) var completedSendID: UUID?
    @Published private(set) var openedNotificationID: UUID?
    @Published var receiveProgress: Double?
    private var transferTask: Task<Void, Never>?
    private lazy var inboxDirectory: URL = {
        if let entries = try? FileManager.default.contentsOfDirectory(at: FileManager.default.temporaryDirectory, includingPropertiesForKeys: nil) {
            for entry in entries where entry.lastPathComponent.hasPrefix("ClipSend-Inbox-") { try? FileManager.default.removeItem(at: entry) }
        }
        let root = FileManager.default.temporaryDirectory.appendingPathComponent("ClipSend-Inbox-" + UUID().uuidString, isDirectory: true)
        try? FileManager.default.createDirectory(at: root, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        return root
    }()
    @Published var status = "Pair a computer to receive text."
    @Published var pairInvitation: PairingInvitation?
    @Published var pairing = false
    @Published var token: Data?
    @Published var authorization: UNAuthorizationStatus?
    @Published var localNetwork: LocalNetworkCheck.State = .idle
    @Published var setupStarted = false
    @Published var skipAlerts = false
    @Published var preparingSetup = false
    @Published var justPaired = false
    private let networkCheck = LocalNetworkCheck()
    var alertsEnabled: Bool { authorization == .authorized || authorization == .provisional || authorization == .ephemeral }
    var setupReady: Bool { (alertsEnabled || skipAlerts) && localNetwork == .allowed }
    func startSetup() async {
        guard !preparingSetup else { return }
        preparingSetup = true; setupStarted = true
        defer { preparingSetup = false }
        await refreshPermission()
        if authorization == .notDetermined { await enableNotifications() }
        if alertsEnabled || skipAlerts { checkLocalNetwork() }
    }
    func continueWithoutAlerts() { skipAlerts = true; setupStarted = true; checkLocalNetwork() }
    func checkLocalNetwork() {
        guard localNetwork != .checking else { return }
        networkCheck.start { [weak self] state in self?.localNetwork = state }
    }
    func openAppSettings() async { if let url = URL(string: UIApplication.openSettingsURLString) { await UIApplication.shared.open(url) } }

    @Published var isDiscovering = false
    @Published var errorMessage: String?
    private let store = SecureStore()
    private var browser: ComputerBrowser?
    private var active = false
    private var storeAvailable = true
    private var seen: [TransferValidator.Key: Date] = [:]
    private struct ReceiveJob: Hashable { let computerID: String; let transferID: String?; let endpoints: [String] }
    private var receiveQueue: [ReceiveJob] = []
    private struct DiscoveredSession { let url: URL; let expiresAt: Date; let receiveEnabled: Bool }
    private var discoveredEndpoints: [String: [String: DiscoveredSession]] = [:]
    private var receiveTask: Task<Void, Never>?
    private var tokenHex: String? { token?.map { String(format: "%02x", $0) }.joined() }

    init() { do { computers = try store.load() } catch { storeAvailable = false; status = "Saved pairings could not be read securely. Existing data was left untouched." } }
    func launched() { UIApplication.shared.registerForRemoteNotifications(); Task { await refreshPermission() } }
    func refreshPushRegistration() { token = nil; status = "Refreshing push registration…"; UIApplication.shared.registerForRemoteNotifications() }
    func registered(token: Data) { self.token = token; if active { refreshKnownSessions() } }
    func registrationFailed() { status = "Push registration unavailable. Foreground receiving still works." }
    func refreshPermission() async { authorization = await UNUserNotificationCenter.current().notificationSettings().authorizationStatus }
    func enableNotifications() async {
        do { _ = try await UNUserNotificationCenter.current().requestAuthorization(options: [.alert,.sound,.badge]); await refreshPermission(); UIApplication.shared.registerForRemoteNotifications() }
        catch { errorMessage = "Couldn’t request notification permission. You can change it in Settings." }
    }
    func openSettings() async { if let u = URL(string: UIApplication.openNotificationSettingsURLString) { _ = await UIApplication.shared.open(u) } }
    func sceneChanged(active: Bool) {
        self.active = active
        if active {
            Task {
                await refreshPermission()
                if setupStarted && !preparingSetup && localNetwork != .allowed && (alertsEnabled || skipAlerts) { checkLocalNetwork() }
            }
            startDiscovery(); drainReceives()
        }
        else { stopDiscovery() }
    }
    func acceptInvitation(_ raw: String) {
        do { pairInvitation = try PairingInvitation.parse(raw); errorMessage = nil }
        catch { errorMessage = error.localizedDescription }
    }
    func pair(deviceName: String) async {
        guard let invitation = pairInvitation else { return }
        guard storeAvailable else { status = "Secure storage is unavailable. Existing pairings were preserved."; return }
        pairing = true; errorMessage = nil; defer { pairing = false }
        if localNetwork != .allowed {
            checkLocalNetwork()
            for _ in 0..<300 {
                if localNetwork == .allowed || pairInvitation == nil { break }
                try? await Task.sleep(for: .milliseconds(100))
            }
            guard pairInvitation != nil, localNetwork == .allowed else { status = "Allow Local Network in Settings, then retry pairing."; return }
        }
        do {
            if token == nil {
                status = "Preparing this device…"
                UIApplication.shared.registerForRemoteNotifications()
                for _ in 0..<100 {
                    if token != nil || pairInvitation == nil { break }
                    try? await Task.sleep(for: .milliseconds(100))
                }
            }
            guard pairInvitation != nil else { return }
            guard let tokenHex, let bundleID = Bundle.main.bundleIdentifier, !bundleID.isEmpty else { status = "Push registration is not ready. Try refreshing it, then pair again."; return }
            let name = deviceName.trimmingCharacters(in: .whitespacesAndNewlines)
            guard !name.isEmpty else { throw PairingError.invalidDetails }
            let client = PinnedClient(fingerprint: invitation.certificateSHA256)
            defer { client.close() }
            var request = URLRequest(url: invitation.endpoint.appendingPathComponent("v1/pair"), timeoutInterval: 120); request.httpMethod = "POST"
            request.setValue("application/json", forHTTPHeaderField: "Content-Type")
            let env = try BuildConfiguration.environment()
            struct Body: Encodable { let invitation, deviceName: String; let deviceToken, bundleID: String; let environment: APNSEnvironment }
            request.httpBody = try JSONEncoder().encode(Body(invitation: invitation.invitation, deviceName: name,
                deviceToken: tokenHex, bundleID: bundleID, environment: env))
            status = "Requesting pairing with \(invitation.computerName)…"
            let (data, response) = try await readResponse(request, client: client, limit: 16 * 1024)
            guard response.statusCode == 201 else { throw ReceiverError.http(response.statusCode) }
            let result = try JSONDecoder().decode(PairResponse.self, from: data)
            guard result.version == 1, result.computerID == invitation.computerID, !result.secret.isEmpty else { throw ReceiverError.invalidResponse }
            let c = PairedComputer(computerID: result.computerID, name: result.computerName,
                                   certificateSHA256: invitation.certificateSHA256, deviceID: result.deviceID, secret: result.secret)
            var updated = computers.filter { $0.computerID != c.computerID }; updated.append(c)
            guard store.save(updated) else { throw ReceiverError.storage }
            computers = updated
            justPaired = true; pairInvitation = nil; status = "Paired with \(c.name). Waiting for text…"; startDiscovery()
        } catch { status = error.localizedDescription; errorMessage = error.localizedDescription }
    }
    func rename(_ computer: PairedComputer, to value: String) -> Bool {
        guard storeAvailable, let i = computers.firstIndex(where: { $0.id == computer.id }) else { status = "Secure storage is unavailable."; return false }
        let name = value.trimmingCharacters(in: .whitespacesAndNewlines); guard !name.isEmpty else { return false }
        var updated = computers; updated[i].name = name
        guard store.save(updated) else { status = "Couldn’t save the computer name in Keychain."; return false }
        computers = updated; return true
    }
    func forget(_ computer: PairedComputer) -> Bool {
        guard storeAvailable else { status = "Secure storage is unavailable."; return false }
        let updated = computers.filter { $0.id != computer.id }
        guard store.save(updated) else { status = "Couldn’t remove this computer from Keychain."; return false }
        computers = updated; if computers.isEmpty { status = "Pair a computer to receive text." }; return true
    }
    func clear(_ message: ReceivedText) {
        guard messages.contains(where: { $0.id == message.id }) else { return }
        beginClear(.received(message))
        messages.removeAll { $0.id == message.id }
    }
    func clearAll() { for message in messages { clear(message) } }
    func clearDraft(text: String, files: [URL]) { beginClear(.draft(text: text, files: files)) }
    private func beginClear(_ content: PendingClear.Content) {
        clearTask?.cancel()
        let pending = PendingClear(contents: (pendingClear?.contents ?? []) + [content])
        pendingClear = pending
        clearTask = Task { [weak self] in
            do { try await Task.sleep(for: .seconds(8)) }
            catch { return } // Cancellation must never fall through to file deletion.
            guard !Task.isCancelled, self?.pendingClear?.id == pending.id else { return }
            self?.commitClear()
        }
    }
    func undoClear() -> [PendingClear.Content]? {
        guard let pending = pendingClear else { return nil }
        clearTask?.cancel(); clearTask = nil; pendingClear = nil
        for content in pending.contents {
            if case .received(let message) = content { messages.append(message) }
        }
        messages.sort { $0.receivedAt > $1.receivedAt }
        return pending.contents
    }
    private func commitClear() {
        clearTask?.cancel(); clearTask = nil
        guard let pending = pendingClear else { return }
        pendingClear = nil
        let files = pending.contents.flatMap { content -> [URL] in
            switch content {
            case .received(let message): return message.attachments.map(\.fileURL)
            case .draft(_, let urls): return urls
            }
        }
        // Every staged attachment owns a unique child directory. Never remove the
        // shared outbox root: a new draft/send may already be using it.
        for file in files { try? FileManager.default.removeItem(at: file.deletingLastPathComponent()) }
    }
    func cancelTransfer() { transferTask?.cancel(); transferStatus = "Cancelling transfer…" }
    func cancelReceive() { receiveTask?.cancel(); receiveProgress = nil; status = "Receive cancelled. Tap Check now to retry." }
    func send(text: String, files: [URL], to computerID: String, finished: (() -> Void)? = nil) {
        guard transferTask == nil, let computer = computers.first(where: { $0.computerID == computerID }) else { finished?(); return }
        transferProgress = 0; transferStatus = "Preparing send…"
        transferTask = Task { [weak self] in
            defer { finished?() }
            guard let self else { return }
            defer { self.transferTask = nil; self.transferProgress = nil }
            do {
                try await self.sendTo(computer, text: text, files: files)
                self.transferStatus = "Received by \(computer.name)."
                self.completedSendID = UUID()
            }
            catch is CancellationError { self.transferStatus = "Transfer cancelled. You can retry when the computer is available." }
            catch { self.transferStatus = "Couldn’t send. \(error.localizedDescription)" }
        }
    }
    private func sendTo(_ computer: PairedComputer, text: String, files: [URL]) async throws {
        let candidates = discoveredEndpoints[computer.computerID, default: [:]].values.filter(\.receiveEnabled)
        guard let endpoint = candidates.first?.url else { throw ReceiverError.noReceiveEndpoint }
        guard text.utf8.count <= 64_000, files.count <= 16, !text.isEmpty || !files.isEmpty else { throw ReceiverError.invalidResponse }
        struct Item: Encodable { let id, name, mime: String; let size: Int64; let sha256: String }
        struct Create: Encodable { let version: Int; let text: String; let attachments: [Item] }
        struct Created: Decodable { let id: String }
        let prepared = try files.map { url -> (Item, URL) in
            let values = try url.resourceValues(forKeys: [.fileSizeKey, .contentTypeKey])
            guard let size = values.fileSize, size <= 128 * 1024 * 1024 else { throw ReceiverError.invalidResponse }
            let name = url.lastPathComponent
            let mime = values.contentType?.preferredMIMEType ?? "application/octet-stream"
            return (Item(id: UUID().uuidString.replacingOccurrences(of: "-", with: ""), name: name, mime: mime, size: Int64(size), sha256: try FileDigest.sha256(url)), url)
        }
        let totalBytes = prepared.reduce(Int64(0), { $0 + $1.0.size })
        guard totalBytes <= 256 * 1024 * 1024 else { throw ReceiverError.invalidResponse }
        let client = PinnedClient(fingerprint: computer.certificateSHA256); defer { client.close() }
        var request = URLRequest(url: endpoint.appendingPathComponent("v1/incoming"), timeoutInterval: 30)
        request.httpMethod = "POST"; request.setValue("Bearer \(computer.secret)", forHTTPHeaderField: "Authorization"); request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.httpBody = try JSONEncoder().encode(Create(version: 1, text: text, attachments: prepared.map(\.0)))
        transferStatus = "Connecting to \(computer.name)…"
        let (data, response) = try await readResponse(request, client: client, limit: 16 * 1024)
        guard response.statusCode == 201 else { throw ReceiverError.http(response.statusCode) }
        let created = try JSONDecoder().decode(Created.self, from: data)
        guard safeProtocolID(created.id) else { throw ReceiverError.invalidResponse }
        do {
            for (index, entry) in prepared.enumerated() {
                try Task.checkCancellation()
                transferStatus = "Sending file \(index + 1) of \(prepared.count)…"
                let offset = prepared.prefix(index).reduce(Int64(0)) { $0 + $1.0.size }
                client.uploadProgress = { [weak self] sent, _ in
                    Task { @MainActor in self?.transferProgress = totalBytes == 0 ? 0 : min(1, Double(offset + sent) / Double(totalBytes)) }
                }
                var upload = URLRequest(url: endpoint.appendingPathComponent("v1/incoming/\(created.id)/items/\(entry.0.id)"), timeoutInterval: 120)
                upload.httpMethod = "PUT"; upload.setValue("Bearer \(computer.secret)", forHTTPHeaderField: "Authorization"); upload.setValue("application/octet-stream", forHTTPHeaderField: "Content-Type")
                let (_, reply) = try await client.session.upload(for: upload, fromFile: entry.1, delegate: client)
                guard (reply as? HTTPURLResponse)?.statusCode == 204 else { throw ReceiverError.invalidResponse }
                transferProgress = Double(index + 1) / Double(max(1, prepared.count))
            }
            try Task.checkCancellation()
            var complete = URLRequest(url: endpoint.appendingPathComponent("v1/incoming/\(created.id)/complete"), timeoutInterval: 30)
            complete.httpMethod = "POST"; complete.setValue("Bearer \(computer.secret)", forHTTPHeaderField: "Authorization")
            let (_, done) = try await readResponse(complete, client: client, limit: 0)
            guard done.statusCode == 204 else { throw ReceiverError.invalidResponse }
        } catch {
            var cancel = URLRequest(url: endpoint.appendingPathComponent("v1/incoming/\(created.id)"), timeoutInterval: 10); cancel.httpMethod = "DELETE"; cancel.setValue("Bearer \(computer.secret)", forHTTPHeaderField: "Authorization")
            _ = try? await readResponse(cancel, client: client, limit: 0)
            throw error
        }
    }
    private func safeProtocolID(_ value: String) -> Bool { !value.isEmpty && value.range(of: "^[A-Za-z0-9_-]+$", options: .regularExpression) != nil }
    func notificationReceived(_ metadata: PushMetadata, opened: Bool = false) {
        if opened { openedNotificationID = UUID() }
        receive(metadata.computerID, transferID: metadata.transferID, endpoints: metadata.endpoints)
    }
    func receive(_ computerID: String, transferID: String? = nil, endpoints: [String]? = nil) {
        let job = ReceiveJob(computerID: computerID, transferID: transferID, endpoints: endpoints ?? [])
        guard !receiveQueue.contains(where: { queued in
            guard queued.computerID == job.computerID, queued.transferID == job.transferID else { return false }
            if let transferID = job.transferID { return queued.transferID == transferID }
            return Set(queued.endpoints) == Set(job.endpoints)
        }) else { return }
        guard receiveQueue.count < 32 else { status = "The receive queue is full. Open ClipSend and check again after processing current sends."; return }
        receiveQueue.append(job)
        guard active else { status = "Ready to receive when ClipSend opens."; return }
        drainReceives()
    }
    private func drainReceives() {
        guard active, receiveTask == nil, !receiveQueue.isEmpty else { return }
        let job = receiveQueue.removeFirst()
        guard let computer = computers.first(where: { $0.computerID == job.computerID }) else { status = "This send came from an unknown computer. Pair it in Settings."; drainReceives(); return }
        receiveProgress = nil
        receiveTask = Task { await receiveFrom(computer, transferID: job.transferID, endpoints: job.endpoints); receiveTask = nil; receiveProgress = nil; drainReceives() }
    }
    private func receiveFrom(_ computer: PairedComputer, transferID: String? = nil, endpoints: [String]? = nil) async {
        let client = PinnedClient(fingerprint: computer.certificateSHA256)
        defer { client.close() }
        pruneExpiredSessions()
        // Pairing listeners are ephemeral; never reconnect to a saved pairing port.
        var baseURLs = discoveredEndpoints[computer.computerID, default: [:]].values.map(\.url)
        seen = seen.filter { $0.value > .now }
        if let endpoints { baseURLs = endpoints.compactMap(URL.init(string:)).filter { ProtocolEndpoint.isAllowed($0) } + baseURLs }
        var tried = Set<URL>()
        var latestError: Error?
        var completedTransfer = false
        for base in baseURLs {
            guard tried.insert(base).inserted else { continue }
            var fetchingTransfer = false
            do {
                    let data = try await get(base.appendingPathComponent("v1/transfers"), client: client, secret: computer.secret)
                    let list = try JSONDecoder().decode(TransferList.self, from: data)
                    guard list.version == 1 else { continue }
                    await refreshToken(for: computer, endpoint: base, client: client)
                    let candidates = transferID.flatMap { id in list.transfers.filter { $0.id == id } } ?? list.transfers
                    for item in candidates {
                        guard safeProtocolID(item.id) else { continue }
                        fetchingTransfer = true
                        let payloadData = try await get(base.appendingPathComponent("v1/transfers/\(item.id)"), client: client, secret: computer.secret)
                    let payload = try JSONDecoder().decode(TransferPayload.self, from: payloadData)
                        guard payload.id == item.id, TransferValidator.validate(payload, computerID: computer.computerID) else { throw ReceiverError.invalidResponse }
                        let key = TransferValidator.key(computerID: computer.computerID, transferID: payload.id)
                        if seen[key] == nil {
                            guard retainedMessages.count < 32 else { status = "The inbox is full. Clear a received item and wait for Undo to finish before receiving another send."; continue }
                            justPaired = false
                            let storedAttachments = try await downloadAttachments(payload.attachments ?? [], transferID: payload.id, endpoint: base, client: client, secret: computer.secret)
                            messages.insert(ReceivedText(computerID: computer.computerID, transferID: payload.id, senderName: payload.senderName, text: payload.text, receivedAt: .now, attachments: storedAttachments), at: 0)
                            seen[key] = ProtocolDate.parse(payload.expiresAt)
                        }
                        var ack = URLRequest(url: base.appendingPathComponent("v1/transfers/\(item.id)/ack")); ack.httpMethod = "POST"
                        ack.setValue("Bearer \(computer.secret)", forHTTPHeaderField: "Authorization")
                        let (_, ackResponse) = try await readResponse(ack, client: client, limit: 0)
                        guard ackResponse.statusCode == 204 else { status = "Text is safe in ClipSend, but the computer hasn’t confirmed receipt yet."; continue }
                        completedTransfer = true
                        fetchingTransfer = false
                        status = payload.text.isEmpty ? "Received files from \(payload.senderName)." : "Received text from \(payload.senderName)."
                    }
            } catch { if Task.isCancelled { latestError = CancellationError(); break }; if fetchingTransfer { latestError = error }; continue }
        }
        if let latestError { status = recoveryMessage(latestError) }
        else if !completedTransfer {
            status = transferID == nil ? "No active sends from \(computer.name). Try again when it’s online." : "That send is no longer available. Ask the computer to send it again."
        }
    }
    private func downloadAttachments(_ descriptors: [AttachmentDescriptor], transferID: String, endpoint: URL, client: PinnedClient, secret: String) async throws -> [ReceivedAttachment] {
        let currentBytes = retainedMessages.flatMap(\.attachments).reduce(Int64(0)) { $0 + $1.descriptor.size }
        let incomingBytes = descriptors.reduce(Int64(0)) { $0 + $1.size }
        guard descriptors.count <= 16, descriptors.allSatisfy(AttachmentDescriptor.isValid), incomingBytes <= 256 * 1024 * 1024, currentBytes + incomingBytes <= 256 * 1024 * 1024 else { throw ReceiverError.inboxLimit }
        var files: [ReceivedAttachment] = []
        receiveProgress = incomingBytes == 0 ? nil : 0
        do {
            for descriptor in descriptors {
                try Task.checkCancellation()
                guard safeProtocolID(transferID) else { throw ReceiverError.invalidResponse }
                var request = URLRequest(url: endpoint.appendingPathComponent("v1/transfers/\(transferID)/items/\(descriptor.id)"), timeoutInterval: 120)
                request.setValue("Bearer \(secret)", forHTTPHeaderField: "Authorization")
                let (stream, response) = try await client.session.bytes(for: request, delegate: client)
                guard let http = response as? HTTPURLResponse, http.statusCode == 200,
                      http.value(forHTTPHeaderField: "Content-Length").flatMap(Int64.init).map({ $0 == descriptor.size }) ?? true else { stream.task.cancel(); throw ReceiverError.invalidResponse }
                let privateItemDir = inboxDirectory.appendingPathComponent(UUID().uuidString, isDirectory: true)
                try FileManager.default.createDirectory(at: privateItemDir, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
                let destination = privateItemDir.appendingPathComponent(SafeFilename.sanitize(descriptor.name))
                guard FileManager.default.createFile(atPath: destination.path, contents: nil, attributes: [.posixPermissions: 0o600]) else { stream.task.cancel(); try? FileManager.default.removeItem(at: privateItemDir); throw ReceiverError.storage }
                let handle: FileHandle
                do { handle = try FileHandle(forWritingTo: destination) } catch { try? FileManager.default.removeItem(at: privateItemDir); throw error }
                defer { try? handle.close() }
                var hash = SHA256(); var buffer = Data(); buffer.reserveCapacity(64 * 1024); var count: Int64 = 0
                do {
                    try await withTaskCancellationHandler {
                      for try await byte in stream {
                        try Task.checkCancellation(); count += 1
                        guard count <= descriptor.size else { stream.task.cancel(); throw ReceiverError.invalidResponse }
                        buffer.append(byte)
                        if buffer.count >= 64 * 1024 { hash.update(data: buffer); try handle.write(contentsOf: buffer); buffer.removeAll(keepingCapacity: true); receiveProgress = incomingBytes == 0 ? nil : min(1, Double(files.reduce(Int64(0), { $0 + $1.descriptor.size }) + count) / Double(incomingBytes)) }
                      }
                    } onCancel: { stream.task.cancel() }
                    if !buffer.isEmpty { hash.update(data: buffer); try handle.write(contentsOf: buffer) }
                    try handle.close()
                } catch { stream.task.cancel(); try? FileManager.default.removeItem(at: privateItemDir); throw error }
                let digest = hash.finalize().map { String(format: "%02x", $0) }.joined()
                guard count == descriptor.size, digest == descriptor.sha256 else { try? FileManager.default.removeItem(at: privateItemDir); throw ReceiverError.invalidResponse }
                files.append(ReceivedAttachment(descriptor: descriptor, fileURL: destination))
            }
            return files
        } catch { files.forEach { try? FileManager.default.removeItem(at: $0.fileURL.deletingLastPathComponent()) }; throw error }
    }
    private func get(_ url: URL, client: PinnedClient, secret: String) async throws -> Data {
        var request = URLRequest(url: url, timeoutInterval: 12); request.setValue("Bearer \(secret)", forHTTPHeaderField: "Authorization")
        // JSON may encode one UTF-8 byte as six ASCII bytes (for example < or NUL).
        // Bound the envelope separately from the validated 64,000-byte text.
        let (data, response) = try await readResponse(request, client: client, limit: 400 * 1024)
        guard (200..<300).contains(response.statusCode) else { throw ReceiverError.http(response.statusCode) }
        return data
    }
    private func readResponse(_ request: URLRequest, client: PinnedClient, limit: Int) async throws -> (Data, HTTPURLResponse) {
        let (stream, response) = try await client.session.bytes(for: request, delegate: client)
        guard let http = response as? HTTPURLResponse else { stream.task.cancel(); throw ReceiverError.invalidResponse }
        if let length = http.value(forHTTPHeaderField: "Content-Length").flatMap(Int.init), length > limit { stream.task.cancel(); throw ReceiverError.invalidResponse }
        var data = Data(); data.reserveCapacity(min(limit, 4096))
        do {
            for try await byte in stream {
                guard data.count < limit else { stream.task.cancel(); throw ReceiverError.invalidResponse }
                data.append(byte)
            }
        } catch { stream.task.cancel(); throw error }
        return (data, http)
    }
    private func refreshToken(for c: PairedComputer, endpoint: URL, client: PinnedClient) async {
        guard let tokenHex, let env = try? BuildConfiguration.environment(), let bundleID = Bundle.main.bundleIdentifier else { return }
        var request = URLRequest(url: endpoint.appendingPathComponent("v1/device/token"), timeoutInterval: 12)
        request.httpMethod = "POST"; request.setValue("Bearer \(c.secret)", forHTTPHeaderField: "Authorization"); request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.httpBody = try? JSONEncoder().encode(TokenBody(deviceToken: tokenHex, bundleID: bundleID, environment: env))
        do { let (_, response) = try await readResponse(request, client: client, limit: 16 * 1024); guard (200..<300).contains(response.statusCode) else { return } }
        catch { /* Token refresh is advisory; the active send still proceeds. */ }
    }
    private func refreshKnownSessions() {
        pruneExpiredSessions()
        for c in computers {
            for session in discoveredEndpoints[c.computerID, default: [:]].values {
                Task {
                    let client = PinnedClient(fingerprint: c.certificateSHA256); defer { client.close() }
                    await refreshToken(for: c, endpoint: session.url, client: client)
                }
            }
        }
    }
    private func startDiscovery() {
        guard active, !computers.isEmpty, !isDiscovering else { return }
        browser = ComputerBrowser(found: { [weak self] id, service, host, port, receiveEnabled in
            Task { @MainActor in
                guard let self, self.computers.contains(where: { $0.computerID == id }), let host,
                      let url = URL(string: "https://\(host.contains(":") ? "[\(host)]" : host):\(port)"), ProtocolEndpoint.isAllowed(url) else { return }
                self.rememberEndpoint(url, for: id, service: service, receiveEnabled: receiveEnabled)
                self.receive(id, endpoints: self.discoveredEndpoints[id]?.values.map { $0.url.absoluteString })
            }
        }, removed: { [weak self] id, service in
            Task { @MainActor in self?.removeDiscoveredService(service, computerID: id) }
        })
        browser?.start(); isDiscovering = true

    }
    private func rememberEndpoint(_ endpoint: URL, for id: String, service: String, receiveEnabled: Bool) {
        pruneExpiredSessions()
        discoveredEndpoints[id, default: [:]][service] = DiscoveredSession(url: endpoint, expiresAt: .now.addingTimeInterval(120), receiveEnabled: receiveEnabled)
        while discoveredEndpoints[id, default: [:]].count > 8, let oldest = discoveredEndpoints[id]?.min(by: { $0.value.expiresAt < $1.value.expiresAt })?.key { discoveredEndpoints[id]?.removeValue(forKey: oldest) }
    }
    private func removeDiscoveredService(_ service: String, computerID: String) { discoveredEndpoints[computerID]?.removeValue(forKey: service) }
    private func pruneExpiredSessions() { for id in discoveredEndpoints.keys { discoveredEndpoints[id] = discoveredEndpoints[id]?.filter { $0.value.expiresAt > .now } } }
    private func stopDiscovery() { browser?.stop(); browser = nil; isDiscovering = false }
    private func recoveryMessage(_ error: Error) -> String {
        if error is CancellationError { return "Receive cancelled. Tap Check now to retry." }
        if let e = error as? ReceiverError, case .http(410) = e { return "That send expired. Ask the computer to send it again." }
        if let e = error as? ReceiverError, case .http(401) = e { return "This computer no longer recognizes this pairing. Forget it and pair again." }
        if let e = error as? ReceiverError, case .inboxLimit = e { return e.localizedDescription }
        return "Couldn’t receive the send. Check that the computer is online, then try again."
    }
    private struct TokenBody: Encodable { let deviceToken: String; let bundleID: String; let environment: APNSEnvironment }
    private enum ReceiverError: LocalizedError {
        case http(Int), invalidResponse, storage, noReceiveEndpoint, inboxLimit
        var errorDescription: String? {
            switch self {
            case .http(401), .http(403): "The computer denied this request. Check its pairing approval and try again."
            case .http(410): "This send or pairing request expired. Create a fresh request on the computer."
            case .http: "The computer returned an unexpected response. Check that it is online and try again."
            case .invalidResponse: "The computer returned data that ClipSend couldn’t safely use."
            case .storage: "ClipSend couldn’t save this pairing in Keychain. No pairing was changed."
            case .noReceiveEndpoint: "This computer is paired but has not enabled receiving. Turn on Receive on the computer and try again."
            case .inboxLimit: "The temporary attachment inbox is full. Clear some received files, then retry."
            }
        }
    }
}

private final class SecureStore {
    private let service = (Bundle.main.bundleIdentifier ?? "dev.cheechnd.clipsend") + ".paired-computers"
    func load() throws -> [PairedComputer] {
        var q: [String: Any] = [kSecClass as String: kSecClassGenericPassword, kSecAttrService as String: service, kSecReturnData as String: true, kSecMatchLimit as String: kSecMatchLimitOne]
        q[kSecAttrAccount as String] = "computers"
        var out: CFTypeRef?; let result = SecItemCopyMatching(q as CFDictionary, &out)
        if result == errSecItemNotFound { return [] }
        guard result == errSecSuccess, let data = out as? Data else { throw NSError(domain: NSOSStatusErrorDomain, code: Int(result)) }
        return try JSONDecoder().decode([PairedComputer].self, from: data)
    }
    func save(_ values: [PairedComputer]) -> Bool {
        guard let data = try? JSONEncoder().encode(values) else { return false }
        let q: [String: Any] = [kSecClass as String: kSecClassGenericPassword, kSecAttrService as String: service, kSecAttrAccount as String: "computers"]
        let attrs: [String: Any] = [kSecValueData as String: data, kSecAttrAccessible as String: kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly]
        let result = SecItemUpdate(q as CFDictionary, attrs as CFDictionary)
        if result == errSecItemNotFound { var add = q; attrs.forEach { add[$0.key] = $0.value }; return SecItemAdd(add as CFDictionary, nil) == errSecSuccess }
        return result == errSecSuccess
    }
}

private final class PinnedClient: NSObject, URLSessionDelegate, URLSessionTaskDelegate, @unchecked Sendable {
    let fingerprint: String; private(set) var session: URLSession!
    var uploadProgress: (@Sendable (Int64, Int64) -> Void)?
    init(fingerprint: String) { self.fingerprint = fingerprint; super.init(); session = URLSession(configuration: .ephemeral, delegate: self, delegateQueue: nil) }
    func close() { session.finishTasksAndInvalidate(); session = nil }
    func urlSession(_ session: URLSession, didReceive challenge: URLAuthenticationChallenge, completionHandler: @escaping (URLSession.AuthChallengeDisposition, URLCredential?) -> Void) {
        guard challenge.protectionSpace.authenticationMethod == NSURLAuthenticationMethodServerTrust,
              let trust = challenge.protectionSpace.serverTrust,
              let chain = SecTrustCopyCertificateChain(trust) as? [SecCertificate], let cert = chain.first else { completionHandler(.cancelAuthenticationChallenge, nil); return }
        let der = SecCertificateCopyData(cert) as Data
        guard Fingerprint.hex(der) == fingerprint,
              SecTrustSetAnchorCertificates(trust, [cert] as CFArray) == errSecSuccess,
              SecTrustSetAnchorCertificatesOnly(trust, true) == errSecSuccess,
              SecTrustSetPolicies(trust, SecPolicyCreateBasicX509()) == errSecSuccess,
              SecTrustEvaluateWithError(trust, nil) else { completionHandler(.cancelAuthenticationChallenge, nil); return }
        completionHandler(.useCredential, URLCredential(trust: trust))
    }
    // Async streaming requests receive this delegate explicitly so their
    // authentication and redirect callbacks use the same pinned trust policy.
    func urlSession(_ session: URLSession, task: URLSessionTask, didReceive challenge: URLAuthenticationChallenge, completionHandler: @escaping (URLSession.AuthChallengeDisposition, URLCredential?) -> Void) {
        urlSession(session, didReceive: challenge, completionHandler: completionHandler)
    }
    func urlSession(_ session: URLSession, task: URLSessionTask, willPerformHTTPRedirection response: HTTPURLResponse, newRequest request: URLRequest, completionHandler: @escaping (URLRequest?) -> Void) { completionHandler(nil) }
    func urlSession(_ session: URLSession, task: URLSessionTask, didSendBodyData bytesSent: Int64, totalBytesSent: Int64, totalBytesExpectedToSend: Int64) { uploadProgress?(totalBytesSent, totalBytesExpectedToSend) }
}
