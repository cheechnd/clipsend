import Foundation

enum BuildConfiguration {
    /// Explicit configuration, checked against signed entitlements/profile by
    /// scripts/ValidateBuild.swift before installation. Never infer from DEBUG.
    static func environment(bundle: Bundle = .main) throws -> APNSEnvironment {
        guard let value = bundle.object(forInfoDictionaryKey: "ClipSendAPNSEnvironment") as? String,
              let environment = APNSEnvironment(rawValue: value) else {
            throw PairingError.unknownEnvironment
        }
        return environment
    }
}
