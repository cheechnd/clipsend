import Foundation
#if canImport(CryptoKit)
import CryptoKit
#endif
#if canImport(Darwin)
import Darwin
#else
import Glibc
#endif

enum APNSEnvironment: String, Codable { case sandbox, production }

enum PairingError: LocalizedError {
    case unknownEnvironment, invalidDetails, invalidInvitation, expiredInvitation
    var errorDescription: String? {
        switch self {
        case .unknownEnvironment: "Push notification environment is unavailable in this build."
        case .invalidDetails, .invalidInvitation: "That pairing code is invalid. Scan the computer’s ClipSend QR code or paste its contents."
        case .expiredInvitation: "This pairing code has expired. Create a new QR code on your computer."
        }
    }
}

struct PairingInvitation: Decodable {
    let version: Int; let computerID: String; let computerName: String; let endpoint: URL
    let certificateSHA256: String; let invitation: String; let expiresAt: Date
    static func parse(_ raw: String, now: Date = .now) throws -> PairingInvitation {
        guard raw.utf8.count <= 4096 else { throw PairingError.invalidInvitation }
        let decoder = JSONDecoder(); decoder.dateDecodingStrategy = .custom { decoder in
            let value = try decoder.singleValueContainer().decode(String.self)
            guard let date = ProtocolDate.parse(value) else { throw PairingError.invalidInvitation }
            return date
        }
        guard let data = raw.data(using: .utf8), let v = try? decoder.decode(Self.self, from: data),
              v.version == 1, !v.computerID.isEmpty, !v.computerName.isEmpty,
              ProtocolEndpoint.isAllowed(v.endpoint),
              v.certificateSHA256.range(of: "^[0-9a-f]{64}$", options: .regularExpression) != nil,
              !v.invitation.isEmpty else { throw PairingError.invalidInvitation }
        guard v.expiresAt > now else { throw PairingError.expiredInvitation }
        return v
    }
}

enum ProtocolDate {
    static func parse(_ value: String) -> Date? {
        let fractional = ISO8601DateFormatter(); fractional.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        if let d = fractional.date(from: value) { return d }
        let seconds = ISO8601DateFormatter(); seconds.formatOptions = [.withInternetDateTime]
        return seconds.date(from: value)
    }
}

enum ProtocolEndpoint {
    static func isAllowed(_ url: URL) -> Bool {
        guard let c = URLComponents(url: url, resolvingAgainstBaseURL: false), c.scheme?.lowercased() == "https",
              c.user == nil, c.password == nil, c.query == nil, c.fragment == nil,
              c.path.isEmpty || c.path == "/", let port = c.port, (1...65535).contains(port), let host = c.host else { return false }
        var addr = in_addr()
        guard host.withCString({ inet_pton(AF_INET, $0, &addr) }) == 1 else { return false }
        let ip = UInt32(bigEndian: addr.s_addr)
        let a = UInt8((ip >> 24) & 255), b = UInt8((ip >> 16) & 255)
        return a == 10 || (a == 172 && (16...31).contains(b)) || (a == 192 && b == 168) || (a == 169 && b == 254)
    }
}

enum TransferValidator {
    struct Key: Hashable { let computerID: String; let transferID: String }
    static func key(computerID: String, transferID: String) -> Key { Key(computerID: computerID, transferID: transferID) }
    static func validate(_ transfer: TransferPayload, computerID: String, now: Date = .now) -> Bool {
        transfer.version == 1 && transfer.computerID == computerID && !transfer.id.isEmpty &&
        !transfer.text.isEmpty && transfer.text.utf8.count <= 64_000 &&
        ProtocolDate.parse(transfer.expiresAt).map { $0 > now } == true
    }
}

struct PushMetadata: Decodable {
    let version: Int
    let computerID: String
    let transferID: String
    let endpoints: [String]
    static func parse(_ userInfo: [AnyHashable: Any]) -> PushMetadata? {
        guard let payload = userInfo["clipsend"] as? [String: Any], JSONSerialization.isValidJSONObject(payload),
              let data = try? JSONSerialization.data(withJSONObject: payload),
              let value = try? JSONDecoder().decode(Self.self, from: data), value.version == 1,
              !value.computerID.isEmpty, !value.transferID.isEmpty, !value.endpoints.isEmpty,
              value.endpoints.allSatisfy({ URL(string: $0).map(ProtocolEndpoint.isAllowed) == true }) else { return nil }
        return value
    }
}

struct PairedComputer: Codable, Identifiable, Equatable {
    var computerID: String; var name: String
    var certificateSHA256: String; var deviceID: String; var secret: String
    var id: String { computerID }
}

struct PairResponse: Decodable { let version: Int; let computerID: String; let computerName: String; let deviceID: String; let secret: String }
struct TransferList: Decodable { let version: Int; let transfers: [TransferStub] }
struct TransferStub: Decodable, Identifiable { let id: String; let expiresAt: String }
struct TransferPayload: Decodable { let version: Int; let id: String; let computerID: String; let senderName: String; let text: String; let expiresAt: String }

struct ReceivedText: Identifiable, Equatable {
    let computerID: String; let transferID: String; let senderName: String; let text: String; let receivedAt: Date
    var id: String { computerID + ":" + transferID }
}

enum Fingerprint {
    static func hex(_ data: Data) -> String {
        #if canImport(CryptoKit)
        return SHA256.hash(data: data).map { String(format: "%02x", $0) }.joined()
        #else
        return PortableSHA256.hex(data)
        #endif
    }
}

private enum PortableSHA256 {
    private static let k: [UInt32] = [0x428a2f98,0x71374491,0xb5c0fbcf,0xe9b5dba5,0x3956c25b,0x59f111f1,0x923f82a4,0xab1c5ed5,0xd807aa98,0x12835b01,0x243185be,0x550c7dc3,0x72be5d74,0x80deb1fe,0x9bdc06a7,0xc19bf174,0xe49b69c1,0xefbe4786,0x0fc19dc6,0x240ca1cc,0x2de92c6f,0x4a7484aa,0x5cb0a9dc,0x76f988da,0x983e5152,0xa831c66d,0xb00327c8,0xbf597fc7,0xc6e00bf3,0xd5a79147,0x06ca6351,0x14292967,0x27b70a85,0x2e1b2138,0x4d2c6dfc,0x53380d13,0x650a7354,0x766a0abb,0x81c2c92e,0x92722c85,0xa2bfe8a1,0xa81a664b,0xc24b8b70,0xc76c51a3,0xd192e819,0xd6990624,0xf40e3585,0x106aa070,0x19a4c116,0x1e376c08,0x2748774c,0x34b0bcb5,0x391c0cb3,0x4ed8aa4a,0x5b9cca4f,0x682e6ff3,0x748f82ee,0x78a5636f,0x84c87814,0x8cc70208,0x90befffa,0xa4506ceb,0xbef9a3f7,0xc67178f2]
    private static func rotate(_ x: UInt32, _ n: UInt32) -> UInt32 { (x >> n) | (x << (32 - n)) }
    static func hex(_ source: Data) -> String {
        var bytes = [UInt8](source); let bits = UInt64(bytes.count) &* 8; bytes.append(0x80)
        while bytes.count % 64 != 56 { bytes.append(0) }
        withUnsafeBytes(of: bits.bigEndian) { bytes.append(contentsOf: $0) }
        var h: [UInt32] = [0x6a09e667,0xbb67ae85,0x3c6ef372,0xa54ff53a,0x510e527f,0x9b05688c,0x1f83d9ab,0x5be0cd19]
        for offset in stride(from: 0, to: bytes.count, by: 64) {
            var w = [UInt32](repeating: 0, count: 64)
            for i in 0..<16 { let j = offset + i * 4; w[i] = UInt32(bytes[j]) << 24 | UInt32(bytes[j+1]) << 16 | UInt32(bytes[j+2]) << 8 | UInt32(bytes[j+3]) }
            for i in 16..<64 { let x = w[i-15], y = w[i-2]; w[i] = w[i-16] &+ (rotate(x,7) ^ rotate(x,18) ^ (x >> 3)) &+ w[i-7] &+ (rotate(y,17) ^ rotate(y,19) ^ (y >> 10)) }
            var a=h[0],b=h[1],c=h[2],d=h[3],e=h[4],f=h[5],g=h[6],hh=h[7]
            for i in 0..<64 { let t1 = hh &+ (rotate(e,6)^rotate(e,11)^rotate(e,25)) &+ ((e & f) ^ (~e & g)) &+ k[i] &+ w[i]; let t2 = (rotate(a,2)^rotate(a,13)^rotate(a,22)) &+ ((a & b) ^ (a & c) ^ (b & c)); hh=g;g=f;f=e;e=d &+ t1;d=c;c=b;b=a;a=t1 &+ t2 }
            h[0] &+= a;h[1] &+= b;h[2] &+= c;h[3] &+= d;h[4] &+= e;h[5] &+= f;h[6] &+= g;h[7] &+= hh
        }
        return h.map { String(format: "%08x", $0) }.joined()
    }
}
