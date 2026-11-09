import SwiftUI
import UIKit
import UserNotifications

@MainActor
final class CompanionModel: ObservableObject {
    static let shared = CompanionModel()

    @Published private(set) var authorization: UNAuthorizationStatus?
    @Published private(set) var permissionError: String?
    @Published private(set) var requestingPermission = false
    @Published private(set) var token: Data?
    @Published private(set) var registrationMessage = "Waiting for a fresh device token."
    @Published private(set) var environment: APNSEnvironment?
    @Published private(set) var environmentError: String?
    @Published private(set) var shortcutMessage: String?
    @Published private(set) var openingShortcut = false

    private var sceneIsActive = false
    private var pendingShortcut = false
    private var registrationTimeout: Task<Void, Never>?

    func launched() {
        do {
            environment = try BuildConfiguration.environment()
        } catch {
            environmentError = (error as? PairingError)?.errorDescription
                ?? "Cannot read this build’s APNs configuration. Pairing is unavailable."
        }
        // Registration does not ask for alert permission. Never cache the token.
        refreshToken()
        Task { await refreshPermission() }
    }

    func sceneChanged(active: Bool) {
        sceneIsActive = active
        if active {
            Task { await refreshPermission() }
            openPendingShortcut()
        }
    }

    func refreshPermission() async {
        authorization = await UNUserNotificationCenter.current().notificationSettings().authorizationStatus
    }

    /// Called only by the explicit Enable notifications button.
    func enableNotifications() async {
        guard !requestingPermission else { return }
        requestingPermission = true
        permissionError = nil
        defer { requestingPermission = false }
        do {
            _ = try await UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound, .badge])
            await refreshPermission()
            refreshToken()
        } catch {
            permissionError = "Couldn’t request notification permission. Try again or open Settings."
        }
    }

    func refreshToken() {
        registrationTimeout?.cancel()
        token = nil
        registrationMessage = "Requesting a fresh device token…"
        UIApplication.shared.registerForRemoteNotifications()
        registrationTimeout = Task {
            do { try await Task.sleep(for: .seconds(25)) } catch { return }
            registrationMessage = "Apple hasn’t returned a token. Check your connection, then refresh."
        }
    }

    func registered(token: Data) {
        registrationTimeout?.cancel()
        guard !token.isEmpty else { registrationFailed(); return }
        self.token = token
        registrationMessage = "Device token received."
    }

    func registrationFailed() {
        registrationTimeout?.cancel()
        token = nil
        registrationMessage = "Couldn’t register with Apple. Check your connection and this build’s push signing, then refresh."
    }

    func pairingJSON(deviceName: String) -> String? {
        guard let token, let environment, let bundleID = Bundle.main.bundleIdentifier else { return nil }
        return try? PairingDetails(
            deviceName: deviceName, token: token, bundleID: bundleID, environment: environment
        ).json()
    }

    func pairingStatus(deviceName: String) -> String {
        guard environment != nil else { return "Pairing is unavailable for this build." }
        guard token != nil else { return registrationMessage }
        guard !deviceName.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else {
            return "Enter a device name to share."
        }
        return pairingJSON(deviceName: deviceName) == nil
            ? "Pairing details are unavailable for this build."
            : "Ready to share."
    }

    func notificationTapped() {
        pendingShortcut = true
        openPendingShortcut()
    }

    private func openPendingShortcut() {
        guard sceneIsActive, pendingShortcut, !openingShortcut else { return }
        pendingShortcut = false
        Task { await openLocalSend() }
    }

    func openLocalSend() async {
        guard !openingShortcut else { return }
        openingShortcut = true
        defer { openingShortcut = false }
        guard let url = URL(string: "shortcuts://run-shortcut?name=Open%20LocalSend") else { return }
        let opened = await UIApplication.shared.open(url, options: [:])
        shortcutMessage = opened
            ? "Shortcut requested. If LocalSend didn’t open, check the Open LocalSend shortcut in Shortcuts."
            : "Couldn’t open Shortcuts. Install Shortcuts and create Open LocalSend, then try again."
    }

    func openSettings() async {
        guard let url = URL(string: UIApplication.openNotificationSettingsURLString) else { return }
        if !(await UIApplication.shared.open(url, options: [:])) {
            permissionError = "Open iOS Settings → Notifications → ClipSend to change notification permission."
        }
    }
}
