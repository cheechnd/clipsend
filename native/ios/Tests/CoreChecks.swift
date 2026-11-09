import Foundation

@main
struct CoreChecks {
    static func main() throws {
        let details = try PairingDetails(
            deviceName: "  Work iPad \"A\"\n", token: Data([0, 10, 128, 255]),
            bundleID: "dev.cheechnd.clipsend", environment: .sandbox
        )
        let json = try details.json()
        let object = try JSONSerialization.jsonObject(with: Data(json.utf8)) as! [String: Any]
        precondition(Set(object.keys) == ["version", "deviceName", "deviceToken", "bundleID", "environment"])
        precondition(object["version"] as? Int == 1)
        precondition(object["deviceName"] as? String == "Work iPad \"A\"")
        precondition(object["deviceToken"] as? String == "000a80ff")
        precondition(object["bundleID"] as? String == "dev.cheechnd.clipsend")
        precondition(object["environment"] as? String == "sandbox")
        let signedBundleID = "XTL-1234ABCD.dev.cheechnd.clipsend"
        let signed = try PairingDetails(
            deviceName: "Phone", token: Data([1]), bundleID: signedBundleID, environment: .sandbox
        )
        let signedObject = try JSONSerialization.jsonObject(with: Data(signed.json().utf8)) as! [String: Any]
        precondition(signedObject["bundleID"] as? String == signedBundleID)
        let production = try PairingDetails(
            deviceName: "Phone", token: Data([1]), bundleID: "dev.cheechnd.clipsend", environment: .production
        )
        let productionJSON = try production.json()
        precondition(productionJSON.contains("\"environment\":\"production\""))
        for (name, token, bundleID) in [
            (" \n", Data([1]), "dev.cheechnd.clipsend"),
            ("Phone", Data(), "dev.cheechnd.clipsend"),
            ("Phone", Data([1]), ""),
            ("Phone", Data([1]), " dev.cheechnd.clipsend ")
        ] {
            do {
                _ = try PairingDetails(deviceName: name, token: token, bundleID: bundleID, environment: .sandbox)
                preconditionFailure("Invalid pairing details accepted")
            } catch PairingError.invalidDetails { }
        }
        // Foundation-only host bundle has no configured APNs environment.
        do {
            _ = try BuildConfiguration.environment()
            preconditionFailure("Unconfigured bundle accepted")
        } catch PairingError.unknownEnvironment { }
        print("Pairing schema, token encoding, and missing-configuration checks passed.")
    }
}
