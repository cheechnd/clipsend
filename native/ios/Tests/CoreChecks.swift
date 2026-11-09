import Foundation

@main struct CoreChecks {
    static func main() throws {
        let fractional = "2026-11-09T03:15:40.123456789Z"
        let whole = "2026-11-09T03:15:40Z"
        precondition(ProtocolDate.parse(fractional) != nil, "Go RFC3339Nano timestamp rejected")
        precondition(ProtocolDate.parse(whole) != nil, "whole-second RFC3339 timestamp rejected")
        precondition(Fingerprint.hex(Data("abc".utf8)) == "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad")
        let json = #"{"version":1,"computerID":"desk-1","computerName":"Workstation","endpoint":"https://192.168.1.9:12345","certificateSHA256":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef","invitation":"aabbcc","expiresAt":"2026-11-09T03:15:40.123456789Z"}"#
        let invitation = try PairingInvitation.parse(json, now: ProtocolDate.parse("2026-11-09T03:00:00Z")!)
        precondition(invitation.computerID == "desk-1")
        do { _ = try PairingInvitation.parse(json, now: ProtocolDate.parse("2026-11-09T03:15:40.2Z")!); preconditionFailure("Expired invitation accepted") }
        catch PairingError.expiredInvitation { }
        for rejected in [
            json.replacingOccurrences(of: "192.168.1.9", with: "8.8.8.8"),
            json.replacingOccurrences(of: "https://192.168.1.9:12345", with: "https://user@192.168.1.9:12345"),
            json.replacingOccurrences(of: "https://192.168.1.9:12345", with: "https://192.168.1.9:12345/arbitrary"),
            json.replacingOccurrences(of: "https://192.168.1.9:12345", with: "http://192.168.1.9:12345")
        ] { do { _ = try PairingInvitation.parse(rejected); preconditionFailure("Unsafe pairing endpoint accepted") } catch { } }
        do { _ = try PairingInvitation.parse(String(repeating: "x", count: 4097)); preconditionFailure("Oversized QR accepted") } catch { }
        let transfer = TransferPayload(version: 1, id: "t-1", computerID: "desk-1", senderName: "Workstation", text: "hello", expiresAt: fractional)
        precondition(TransferValidator.validate(transfer, computerID: "desk-1", now: ProtocolDate.parse("2026-11-09T03:00:00Z")!))
        precondition(!TransferValidator.validate(transfer, computerID: "other", now: .now), "Cross-computer transfer accepted")
        let expired = TransferPayload(version: 1, id: "t-2", computerID: "desk-1", senderName: "Workstation", text: "hello", expiresAt: whole)
        precondition(!TransferValidator.validate(expired, computerID: "desk-1", now: ProtocolDate.parse("2026-11-09T03:15:41Z")!))
        let oversized = TransferPayload(version: 1, id: "t-3", computerID: "desk-1", senderName: "Workstation", text: String(repeating: "x", count: 64_001), expiresAt: fractional)
        precondition(!TransferValidator.validate(oversized, computerID: "desk-1", now: .now))
        precondition(TransferValidator.key(computerID: "a:b", transferID: "c") != TransferValidator.key(computerID: "a", transferID: "b:c"))
        print("Protocol date, private endpoint, invitation expiry, transfer validation, and dedup checks passed.")
    }
}
