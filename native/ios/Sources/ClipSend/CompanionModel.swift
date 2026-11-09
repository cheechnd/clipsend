import Foundation
import SwiftUI
import UIKit
import UserNotifications
import Security

@MainActor final class CompanionModel: ObservableObject {
    static let shared = CompanionModel()
    @Published private(set) var computers: [PairedComputer] = []
    @Published private(set) var messages: [ReceivedText] = []
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
    private struct DiscoveredSession { let url: URL; let expiresAt: Date }
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
    func clear(_ message: ReceivedText) { messages.removeAll { $0.id == message.id } }
    func notificationReceived(_ metadata: PushMetadata) { receive(metadata.computerID, transferID: metadata.transferID, endpoints: metadata.endpoints) }
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
        receiveTask = Task { await receiveFrom(computer, transferID: job.transferID, endpoints: job.endpoints); receiveTask = nil; drainReceives() }
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
                        fetchingTransfer = true
                        let payloadData = try await get(base.appendingPathComponent("v1/transfers/\(item.id)"), client: client, secret: computer.secret)
                        let payload = try JSONDecoder().decode(TransferPayload.self, from: payloadData)
                        guard payload.id == item.id, TransferValidator.validate(payload, computerID: computer.computerID) else { throw ReceiverError.invalidResponse }
                        let key = TransferValidator.key(computerID: computer.computerID, transferID: payload.id)
                        if seen[key] == nil {
                            guard messages.count < 32 else { status = "The inbox is full. Clear a received item before receiving another send."; continue }
                            justPaired = false
                            messages.insert(ReceivedText(computerID: computer.computerID, transferID: payload.id, senderName: payload.senderName, text: payload.text, receivedAt: .now), at: 0)
                            seen[key] = ProtocolDate.parse(payload.expiresAt)
                        }
                        var ack = URLRequest(url: base.appendingPathComponent("v1/transfers/\(item.id)/ack")); ack.httpMethod = "POST"
                        ack.setValue("Bearer \(computer.secret)", forHTTPHeaderField: "Authorization")
                        let (_, ackResponse) = try await readResponse(ack, client: client, limit: 0)
                        guard ackResponse.statusCode == 204 else { status = "Text is safe in ClipSend, but the computer hasn’t confirmed receipt yet."; continue }
                        completedTransfer = true
                        fetchingTransfer = false
                        status = "Received text from \(payload.senderName)."
                    }
            } catch { if fetchingTransfer { latestError = error }; continue }
        }
        if let latestError { status = recoveryMessage(latestError) }
        else if !completedTransfer {
            status = transferID == nil ? "No active sends from \(computer.name). Try again when it’s online." : "That send is no longer available. Ask the computer to send it again."
        }
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
        browser = ComputerBrowser(found: { [weak self] id, service, host, port in
            Task { @MainActor in
                guard let self, self.computers.contains(where: { $0.computerID == id }), let host,
                      let url = URL(string: "https://\(host.contains(":") ? "[\(host)]" : host):\(port)"), ProtocolEndpoint.isAllowed(url) else { return }
                self.rememberEndpoint(url, for: id, service: service)
                self.receive(id, endpoints: self.discoveredEndpoints[id]?.values.map { $0.url.absoluteString })
            }
        }, removed: { [weak self] id, service in
            Task { @MainActor in self?.removeDiscoveredService(service, computerID: id) }
        })
        browser?.start(); isDiscovering = true

    }
    private func rememberEndpoint(_ endpoint: URL, for id: String, service: String) {
        pruneExpiredSessions()
        discoveredEndpoints[id, default: [:]][service] = DiscoveredSession(url: endpoint, expiresAt: .now.addingTimeInterval(120))
        while discoveredEndpoints[id, default: [:]].count > 8, let oldest = discoveredEndpoints[id]?.min(by: { $0.value.expiresAt < $1.value.expiresAt })?.key { discoveredEndpoints[id]?.removeValue(forKey: oldest) }
    }
    private func removeDiscoveredService(_ service: String, computerID: String) { discoveredEndpoints[computerID]?.removeValue(forKey: service) }
    private func pruneExpiredSessions() { for id in discoveredEndpoints.keys { discoveredEndpoints[id] = discoveredEndpoints[id]?.filter { $0.value.expiresAt > .now } } }
    private func stopDiscovery() { browser?.stop(); browser = nil; isDiscovering = false }
    private func recoveryMessage(_ error: Error) -> String {
        if let e = error as? ReceiverError, case .http(410) = e { return "That send expired. Ask the computer to send it again." }
        if let e = error as? ReceiverError, case .http(401) = e { return "This computer no longer recognizes this pairing. Forget it and pair again." }
        return "Couldn’t receive the send. Check that the computer is online, then try again."
    }
    private struct TokenBody: Encodable { let deviceToken: String; let bundleID: String; let environment: APNSEnvironment }
    private enum ReceiverError: LocalizedError {
        case http(Int), invalidResponse, storage
        var errorDescription: String? {
            switch self {
            case .http(401), .http(403): "The computer denied this request. Check its pairing approval and try again."
            case .http(410): "This send or pairing request expired. Create a fresh request on the computer."
            case .http: "The computer returned an unexpected response. Check that it is online and try again."
            case .invalidResponse: "The computer returned data that ClipSend couldn’t safely use."
            case .storage: "ClipSend couldn’t save this pairing in Keychain. No pairing was changed."
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
}

private final class ComputerBrowser: NSObject, NetServiceBrowserDelegate, NetServiceDelegate {
    private let browser = NetServiceBrowser(); private var services: [NetService] = []
    private let found: (String, String, String?, Int) -> Void; private let removed: (String, String) -> Void
    private var serviceIDs: [String: String] = [:]
    init(found: @escaping (String, String, String?, Int) -> Void, removed: @escaping (String, String) -> Void) { self.found = found; self.removed = removed; super.init(); browser.delegate = self }
    func start() { browser.searchForServices(ofType: "_clipsend._tcp.", inDomain: "local.") }
    func stop() { browser.stop(); services.forEach { $0.stop() } }
    func netServiceBrowser(_ browser: NetServiceBrowser, didFind service: NetService, moreComing: Bool) { service.delegate = self; services.append(service); service.resolve(withTimeout: 8) }
    func netServiceBrowser(_ browser: NetServiceBrowser, didRemove service: NetService, moreComing: Bool) {
        if let id = serviceIDs.removeValue(forKey: service.name) { removed(id, service.name) }
        services.removeAll { $0.name == service.name }
    }
    func netServiceDidResolveAddress(_ sender: NetService) {
        let attrs = sender.addresses?.compactMap { address -> String? in
            var host = [CChar](repeating: 0, count: Int(NI_MAXHOST)); let ok = address.withUnsafeBytes { raw in getnameinfo(raw.baseAddress!.assumingMemoryBound(to: sockaddr.self), socklen_t(address.count), &host, socklen_t(host.count), nil, 0, NI_NUMERICHOST) == 0 }; guard ok, let zero = host.firstIndex(of: 0) else { return nil }; return String(decoding: host[..<zero].map { UInt8(bitPattern: $0) }, as: UTF8.self)
        } ?? []
        guard let txt = sender.txtRecordData().map(NetService.dictionary(fromTXTRecord:)),
              let version = txt["v"].flatMap({ String(data: $0, encoding: .utf8) }), version == "1",
              let id = txt["computerID"].flatMap({ String(data: $0, encoding: .utf8) }) else { return }
        let host = attrs.first { value in URL(string: "https://\(value.contains(":") ? "[\(value)]" : value):\(sender.port)").map(ProtocolEndpoint.isAllowed) == true }
        guard let host else { return }
        serviceIDs[sender.name] = id
        found(id, sender.name, host, sender.port)
    }
}
