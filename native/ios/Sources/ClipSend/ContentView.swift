import SwiftUI
import UIKit
import UserNotifications

struct ContentView: View {
    @ObservedObject var model: CompanionModel
    @AppStorage("deviceName") private var deviceName = UIDevice.current.name
    @State private var copied = false

    private var pairingJSON: String? { model.pairingJSON(deviceName: deviceName) }

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    LabeledContent("Notifications", value: permissionLabel)
                    if model.authorization == .notDetermined {
                        Button("Enable notifications") {
                            Task { await model.enableNotifications() }
                        }
                        .disabled(model.requestingPermission)
                    } else if model.authorization != nil {
                        Button("Notification settings") { Task { await model.openSettings() } }
                    }
                    if let error = model.permissionError {
                        Text(error).foregroundStyle(.red)
                    }
                } footer: {
                    Text("Get an alert when your computer is ready to send. Tap the alert to open LocalSend.")
                }

                Section {
                    TextField("Device name", text: $deviceName)
                        .textContentType(.name)
                        .submitLabel(.done)
                        .accessibilityLabel("Device name")
                    if let error = model.environmentError {
                        Label(error, systemImage: "exclamationmark.triangle")
                            .foregroundStyle(.red)
                    }
                    Text(model.pairingStatus(deviceName: deviceName))
                        .foregroundStyle(.secondary)
                    if let pairingJSON {
                        ShareLink(item: pairingJSON) {
                            Label("Share pairing details", systemImage: "square.and.arrow.up")
                        }
                        Button {
                            UIPasteboard.general.string = pairingJSON
                            copied = true
                        } label: {
                            Label(copied ? "Copied pairing details" : "Copy pairing details",
                                  systemImage: copied ? "checkmark" : "doc.on.doc")
                        }
                    } else {
                        Label("Share pairing details", systemImage: "square.and.arrow.up")
                            .foregroundStyle(.secondary)
                            .accessibilityHint("Available with a device name, fresh token, and configured push environment")
                    }
                    Button("Refresh device token") { model.refreshToken() }
                    DisclosureGroup("Device details") {
                        LabeledContent("App", value: Bundle.main.bundleIdentifier ?? "Unknown")
                        LabeledContent("Push environment", value: model.environment?.rawValue ?? "Unknown")
                    }
                } header: {
                    Text("Pair this device")
                } footer: {
                    Text("Share the JSON text through LocalSend to your computer. Share again after a token update. ClipSend doesn’t contact a pairing server.")
                }

                Section {
                    Button {
                        Task { await model.openLocalSend() }
                    } label: {
                        Label("Open LocalSend", systemImage: "arrow.up.forward.app")
                    }
                    .disabled(model.openingShortcut)
                    if let message = model.shortcutMessage {
                        Text(message)
                            .foregroundStyle(.secondary)
                    }
                } footer: {
                    Text("One-time setup: in Shortcuts, create “Open LocalSend” with an Open App action set to LocalSend. Run it once to check it works.")
                }
            }
            .navigationTitle("ClipSend")
            .scrollDismissesKeyboard(.interactively)
            .onChange(of: pairingJSON) { _, _ in copied = false }
        }
    }

    private var permissionLabel: String {
        switch model.authorization {
        case .notDetermined: "Not enabled"
        case .denied: "Off in Settings"
        case .authorized: "Enabled"
        case .provisional: "Quiet delivery"
        case .ephemeral: "Temporary"
        case nil: "Checking…"
        @unknown default: "Check Settings"
        }
    }
}
