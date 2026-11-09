import Foundation

enum APNSEnvironment: String, Encodable {
    case sandbox, production
}

enum PairingError: LocalizedError {
    case unknownEnvironment, invalidDetails

    var errorDescription: String? {
        switch self {
        case .unknownEnvironment:
            "APNs environment is unknown. Pairing is unavailable until this build is configured correctly."
        case .invalidDetails:
            "A device name and a fresh APNs token are required to share pairing details."
        }
    }
}

struct PairingDetails: Encodable {
    let version = 1
    let deviceName: String
    let deviceToken: String
    let bundleID: String
    let environment: APNSEnvironment

    init(deviceName: String, token: Data, bundleID: String, environment: APNSEnvironment) throws {
        let name = deviceName.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !name.isEmpty, !token.isEmpty, !bundleID.isEmpty,
              bundleID == bundleID.trimmingCharacters(in: .whitespacesAndNewlines) else {
            throw PairingError.invalidDetails
        }
        self.deviceName = name
        self.deviceToken = token.map { String(format: "%02x", $0) }.joined()
        self.bundleID = bundleID
        self.environment = environment
    }

    func json() throws -> String {
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.sortedKeys]
        return String(decoding: try encoder.encode(self), as: UTF8.self)
    }
}
