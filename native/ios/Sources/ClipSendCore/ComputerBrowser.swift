import Foundation
import Darwin

public final class ComputerBrowser: NSObject, NetServiceBrowserDelegate, NetServiceDelegate {
    private let browser = NetServiceBrowser(); private var services: [NetService] = []
    private let found: (String, String, String?, Int, Bool) -> Void; private let removed: (String, String) -> Void
    private var serviceIDs: [String: String] = [:]
    public init(found: @escaping (String, String, String?, Int, Bool) -> Void, removed: @escaping (String, String) -> Void) { self.found = found; self.removed = removed; super.init(); browser.delegate = self }
    public func start() { browser.searchForServices(ofType: "_clipsend._tcp.", inDomain: "local.") }
    public func stop() { browser.stop(); services.forEach { $0.stop() } }
    public func netServiceBrowser(_ browser: NetServiceBrowser, didFind service: NetService, moreComing: Bool) { guard services.count < 64, !services.contains(where: { $0.name == service.name }) else { return }; service.delegate = self; services.append(service); service.resolve(withTimeout: 8) }
    public func netServiceBrowser(_ browser: NetServiceBrowser, didRemove service: NetService, moreComing: Bool) {
        if let id = serviceIDs.removeValue(forKey: service.name) { removed(id, service.name) }
        services.removeAll { $0.name == service.name }
    }
    public func netServiceDidResolveAddress(_ sender: NetService) {
        let attrs = sender.addresses?.compactMap { address -> String? in
            var host = [CChar](repeating: 0, count: Int(NI_MAXHOST)); let ok = address.withUnsafeBytes { raw in getnameinfo(raw.baseAddress!.assumingMemoryBound(to: sockaddr.self), socklen_t(address.count), &host, socklen_t(host.count), nil, 0, NI_NUMERICHOST) == 0 }; guard ok, let zero = host.firstIndex(of: 0) else { return nil }; return String(decoding: host[..<zero].map { UInt8(bitPattern: $0) }, as: UTF8.self)
        } ?? []
        guard let txt = sender.txtRecordData().map(NetService.dictionary(fromTXTRecord:)),
              let version = txt["v"].flatMap({ String(data: $0, encoding: .utf8) }), version == "1",
              let id = txt["computerID"].flatMap({ String(data: $0, encoding: .utf8) }) else { return }
        let host = attrs.first { value in Self.isPrivateIPv4(value) }
        guard let host else { return }
        serviceIDs[sender.name] = id
        let receiveEnabled = txt["receive"].flatMap { String(data: $0, encoding: .utf8) } == "1"
        found(id, sender.name, host, sender.port, receiveEnabled)
    }
    private static func isPrivateIPv4(_ host: String) -> Bool {
        var address = in_addr()
        guard host.withCString({ inet_pton(AF_INET, $0, &address) }) == 1 else { return false }
        let value = UInt32(bigEndian: address.s_addr)
        let a = UInt8((value >> 24) & 255), b = UInt8((value >> 16) & 255)
        return a == 10 || (a == 172 && (16...31).contains(b)) || (a == 192 && b == 168) || (a == 169 && b == 254)
    }
}
