#!/usr/bin/env swift
import Foundation

// Read-only configuration/profile checks. No signing, installation, account
// access, device enumeration, Mach-O parsing, or profile contents in output.
enum ValidationFailure: Error { case invalid(String) }

func require(_ condition: Bool, _ message: String) throws {
    if !condition { throw ValidationFailure.invalid(message) }
}

func plist(_ data: Data) throws -> [String: Any] {
    guard let dictionary = try PropertyListSerialization.propertyList(from: data, format: nil) as? [String: Any] else {
        throw ValidationFailure.invalid("Expected a property-list dictionary.")
    }
    return dictionary
}

let privateRanges = ["10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "169.254.0.0/16"]
let localATS: [String: Any] = [
    "NSAllowsLocalNetworking": true,
    "NSExceptionDomains": Dictionary(uniqueKeysWithValues: privateRanges.map {
        ($0, ["NSExceptionAllowsInsecureHTTPLoads": true, "NSExceptionMinimumTLSVersion": "TLSv1.2"] as [String: Any])
    }),
]

func configuration(_ info: [String: Any], requested: [String: Any], bundleID: String = "dev.cheechnd.clipsend") throws -> String {
    try require(info["CFBundleIdentifier"] as? String == bundleID, "Built app does not match the exact expected bundle ID.")
    try require(info["CFBundleDisplayName"] as? String == "ClipSend", "Built app has the wrong display name.")
    let environments = ["sandbox": "development", "production": "production"]
    guard let value = info["ClipSendAPNSEnvironment"] as? String, let expected = environments[value] else {
        throw ValidationFailure.invalid("Missing or unknown ClipSendAPNSEnvironment.")
    }
    try require(requested["aps-environment"] as? String == expected, "Info.plist environment and requested aps-environment differ.")
    try require(Set(info["UIDeviceFamily"] as? [Int] ?? []) == [1, 2], "iPhone/iPad support is missing.")
    let ats = info["NSAppTransportSecurity"] as? [String: Any]
    try require(NSDictionary(dictionary: ats ?? [:]).isEqual(to: localATS),
                "Require only private-IP ATS exceptions with TLS 1.2; global arbitrary loads must remain disabled.")
    return expected
}

func checkProfile(_ profile: [String: Any], expected: String, bundleID: String = "dev.cheechnd.clipsend") throws {
    guard let entitlements = profile["Entitlements"] as? [String: Any] else {
        throw ValidationFailure.invalid("Profile has no entitlements.")
    }
    try require(entitlements["aps-environment"] as? String == expected, "Actual profile aps-environment is missing or mismatched.")
    let identifier = entitlements["application-identifier"] as? String
    let prefixes = profile["ApplicationIdentifierPrefix"] as? [String] ?? []
    try require(prefixes.contains { identifier == "\($0).\(bundleID)" }, "Actual profile is not for the exact built App ID.")
    guard let expiry = profile["ExpirationDate"] as? Date, expiry > Date() else {
        throw ValidationFailure.invalid("Profile is expired or has no expiration.")
    }
}

func decodedProfile(_ path: String) throws -> [String: Any] {
    let process = Process()
    process.executableURL = URL(fileURLWithPath: "/usr/bin/env")
    process.arguments = ["openssl", "cms", "-verify", "-inform", "DER", "-noverify", "-in", path]
    let output = Pipe()
    process.standardOutput = output
    process.standardError = FileHandle.nullDevice
    try process.run()
    let data = output.fileHandleForReading.readDataToEndOfFile()
    process.waitUntilExit()
    try require(process.terminationStatus == 0, "OpenSSL could not decode/verify the CMS profile.")
    return try plist(data)
}

func expectFailure(_ action: () throws -> Void) throws {
    do { try action() } catch { return }
    throw ValidationFailure.invalid("A negative validation check unexpectedly succeeded.")
}

func selfCheck() throws {
    var info: [String: Any] = [
        "CFBundleIdentifier": "dev.cheechnd.clipsend", "CFBundleDisplayName": "ClipSend",
        "ClipSendAPNSEnvironment": "sandbox", "UIDeviceFamily": [1, 2],
        "NSAppTransportSecurity": localATS,
    ]
    let development = try configuration(info, requested: ["aps-environment": "development"])
    for invalidATS: [String: Any] in [[:], ["NSAllowsArbitraryLoads": true], ["NSAllowsLocalNetworking": true, "NSAllowsArbitraryLoads": true]] {
        var invalidInfo = info
        invalidInfo["NSAppTransportSecurity"] = invalidATS
        try expectFailure { _ = try configuration(invalidInfo, requested: ["aps-environment": "development"]) }
    }
    try require(development == "development", "Development check failed.")
    info["ClipSendAPNSEnvironment"] = "production"
    let production = try configuration(info, requested: ["aps-environment": "production"])
    try require(production == "production", "Production check failed.")
    for environment in ["production", "unknown", ""] {
        info["ClipSendAPNSEnvironment"] = environment
        try expectFailure { _ = try configuration(info, requested: ["aps-environment": "development"]) }
    }
    let valid: [String: Any] = [
        "ApplicationIdentifierPrefix": ["TEST"], "ExpirationDate": Date().addingTimeInterval(3600),
        "Entitlements": ["application-identifier": "TEST.dev.cheechnd.clipsend", "aps-environment": "development"],
    ]
    try checkProfile(valid, expected: "development")
    let signedID = "XTL-1234ABCD.dev.cheechnd.clipsend"
    info["CFBundleIdentifier"] = signedID
    info["ClipSendAPNSEnvironment"] = "sandbox"
    _ = try configuration(info, requested: ["aps-environment": "development"], bundleID: signedID)
    try expectFailure { _ = try configuration(info, requested: ["aps-environment": "development"]) }
    var signedProfile = valid
    signedProfile["Entitlements"] = ["application-identifier": "TEST.\(signedID)", "aps-environment": "development"]
    try checkProfile(signedProfile, expected: "development", bundleID: signedID)
    try expectFailure { try checkProfile(signedProfile, expected: "development") }
    for invalid in [
        ["ApplicationIdentifierPrefix": ["TEST"], "ExpirationDate": Date().addingTimeInterval(3600),
         "Entitlements": ["application-identifier": "TEST.*", "aps-environment": "development"]],
        ["ApplicationIdentifierPrefix": ["TEST"], "ExpirationDate": Date(timeIntervalSince1970: 0),
         "Entitlements": ["application-identifier": "TEST.dev.cheechnd.clipsend", "aps-environment": "development"]],
        ["ApplicationIdentifierPrefix": ["TEST"], "ExpirationDate": Date().addingTimeInterval(3600),
         "Entitlements": ["application-identifier": "TEST.dev.cheechnd.clipsend", "aps-environment": "production"]],
    ] as [[String: Any]] {
        try expectFailure { try checkProfile(invalid, expected: "development") }
    }
    print("Environment/profile validation checks passed.")
}

do {
    let arguments = Array(CommandLine.arguments.dropFirst())
    if arguments == ["--self-check"] {
        try selfCheck()
    } else {
        guard arguments.count == 1 || (arguments.count == 5 && arguments[1] == "--profile" && arguments[3] == "--expected-bundle-id") else {
            throw ValidationFailure.invalid("Usage: swift scripts/ValidateBuild.swift APP_DIRECTORY [--profile PROFILE --expected-bundle-id EXACT_SIGNED_ID]\n       swift scripts/ValidateBuild.swift --self-check")
        }
        let root = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
        let app = URL(fileURLWithPath: arguments[0])
        let info = try plist(Data(contentsOf: app.appendingPathComponent("Info.plist")))
        let requested = try plist(Data(contentsOf: root.appendingPathComponent("ClipSend.entitlements")))
        let source = try plist(Data(contentsOf: root.appendingPathComponent("Info.plist")))
        let expectedID = arguments.count == 5 ? arguments[4] : "dev.cheechnd.clipsend"
        let expected = try configuration(info, requested: requested, bundleID: expectedID)
        try require(source["ClipSendAPNSEnvironment"] as? String == info["ClipSendAPNSEnvironment"] as? String,
                    "Built app has stale environment configuration. Rebuild.")
        let yaml = try String(contentsOf: root.appendingPathComponent("xtool.yml"), encoding: .utf8)
        try require(yaml.components(separatedBy: .newlines).contains("entitlementsPath: ClipSend.entitlements"),
                    "xtool.yml must use entitlementsPath: ClipSend.entitlements.")
        print("Build configuration matches requested APNs entitlement (\(expected)).")
        if arguments.count == 5 {
            try checkProfile(decodedProfile(arguments[2]), expected: expected, bundleID: expectedID)
            print("Actual embedded profile matches APS environment, exact App ID, and expiration.")
        } else {
            print("Configuration check only; actual signed profile not inspected.")
        }
    }
} catch ValidationFailure.invalid(let message) {
    FileHandle.standardError.write(Data("Validation failed: \(message)\n".utf8))
    exit(1)
} catch {
    FileHandle.standardError.write(Data("Validation failed: could not read input files or run OpenSSL.\n".utf8))
    exit(1)
}
