import SwiftUI
import UIKit

struct ContentView: View {
    @ObservedObject var model: CompanionModel
    @State private var showScanner = false
    @State private var scannedCode: String?
    @State private var showSettings = false
    @State private var pasted = ""
    @State private var showingPaste = false
    @State private var deviceName = UIDevice.current.name
    @State private var copiedID: String?
    private var width: CGFloat { 520 }

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 22) {
                    header
                    if model.computers.isEmpty { onboarding } else { pairedContent }
                    if !model.status.isEmpty { Label(model.status, systemImage: "info.circle").font(.footnote).foregroundStyle(.secondary).frame(maxWidth: .infinity, alignment: .leading) }
                    if let error = model.errorMessage { Text(error).font(.footnote).foregroundStyle(.red).accessibilityAddTraits(.updatesFrequently) }
                }
                .padding(.horizontal, 22).padding(.vertical, 18)
                .frame(maxWidth: width, alignment: .leading).frame(maxWidth: .infinity)
            }
            .background(Color(uiColor: .systemGroupedBackground))
            .navigationTitle("ClipSend").navigationBarTitleDisplayMode(.inline)
            .toolbar { ToolbarItem(placement: .topBarTrailing) { Button { showSettings = true } label: { Image(systemName: "gearshape") }.frame(minWidth: 44, minHeight: 44).contentShape(Rectangle()).accessibilityLabel("Settings") } }
            .sheet(isPresented: $showScanner, onDismiss: { if let code = scannedCode { scannedCode = nil; model.acceptInvitation(code) } }) {
                NavigationStack {
                    QRScanner { scannedCode = $0; showScanner = false }
                        .ignoresSafeArea(edges: .bottom)
                        .navigationTitle("Scan pairing code")
                        .navigationBarTitleDisplayMode(.inline)
                        .toolbar { ToolbarItem(placement: .cancellationAction) { Button("Cancel") { showScanner = false } } }
                }
            }
            .sheet(isPresented: $showSettings) { settingsView.presentationDetents([.medium, .large]) }
            .alert("Paste pairing code", isPresented: $showingPaste) {
                TextField("Pairing JSON", text: $pasted, axis: .vertical).lineLimit(3...6)
                Button("Continue") { model.acceptInvitation(pasted) }
                Button("Cancel", role: .cancel) { }
            } message: { Text("Paste the QR code contents from your computer.") }
            .sheet(item: Binding(get: { model.pairInvitation.map(InvitationSheet.init) }, set: { if $0 == nil { model.pairInvitation = nil } })) { item in
                pairApprovalSheet(item.invitation).presentationDetents([.medium, .large])
            }
        }
    }
    private var header: some View {
        HStack(spacing: 14) {
            Image(systemName: "document.on.clipboard.fill").font(.system(size: 25, weight: .medium)).foregroundStyle(.white).frame(width: 54, height: 54).background(Color.accentColor, in: RoundedRectangle(cornerRadius: 16))
            VStack(alignment: .leading, spacing: 3) { Text("Text, delivered.").font(.title2.bold()); Text("Private, direct, and ready when you are.").font(.subheadline).foregroundStyle(.secondary) }
        }.padding(.top, 8)
    }
    private var onboarding: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text(model.setupReady ? "Connect your computer" : "Set up ClipSend").font(.headline)
            if !model.setupStarted && !model.setupReady {
                Text("Allow notifications to know when text is ready, then local-network access to receive it securely from your computer.").foregroundStyle(.secondary)
                Button("Set up ClipSend") { Task { await model.startSetup() } }
                    .buttonStyle(.borderedProminent).controlSize(.large).disabled(model.preparingSetup)
            } else if !model.alertsEnabled && !model.skipAlerts {
                Label("Notifications", systemImage: "bell.badge").font(.subheadline.bold())
                Text("Allow alerts so you can receive text without opening ClipSend first.").foregroundStyle(.secondary)
                if model.authorization == .denied {
                    Button("Open Settings") { Task { await model.openSettings() } }.buttonStyle(.borderedProminent)
                } else {
                    Button("Allow notifications") { Task { await model.startSetup() } }.buttonStyle(.borderedProminent).disabled(model.preparingSetup)
                }
                Button("Continue without alerts") { model.continueWithoutAlerts() }.font(.subheadline)
            } else if model.localNetwork != .allowed {
                Label("Local network", systemImage: "wifi").font(.subheadline.bold())
                Text("Allow access to find your computer on the same Wi-Fi. Text travels directly between your devices.").foregroundStyle(.secondary)
                switch model.localNetwork {
                case .checking: ProgressView("Checking access…")
                case .denied:
                    Text("Local-network access is off. Turn it on in Settings; setup resumes when you return.").font(.footnote)
                    Button("Open Settings") { Task { await model.openAppSettings() } }.buttonStyle(.borderedProminent)
                case .unavailable:
                    Text("Couldn’t confirm access. Check Wi-Fi and Local Network in Settings, then try again.").font(.footnote)
                    Button("Try again") { model.checkLocalNetwork() }.buttonStyle(.borderedProminent)
                    Button("Open Settings") { Task { await model.openAppSettings() } }
                case .idle: Button("Allow local network") { model.checkLocalNetwork() }.buttonStyle(.borderedProminent)
                case .allowed: EmptyView()
                }
            } else {
                Label("Permissions ready", systemImage: "checkmark.circle.fill").font(.subheadline).foregroundStyle(.green)
                Text("On your computer, run clipsend pair followed by a device name. Scan its QR code, then approve this device there.").foregroundStyle(.secondary)
                Button { showScanner = true } label: { Label("Scan QR code", systemImage: "qrcode.viewfinder") }.buttonStyle(.borderedProminent).controlSize(.large)
                Button("Paste pairing code") { showingPaste = true }.buttonStyle(.bordered)
            }
        }.padding(20).background(.background, in: RoundedRectangle(cornerRadius: 22))
    }
    private var pairedContent: some View {
        VStack(alignment: .leading, spacing: 16) {
            if model.justPaired {
                Label("Computer connected", systemImage: "checkmark.circle.fill").font(.headline)
                Text("Your computer offers a test send. Return to the Home Screen, then start the test there to check notification delivery.").font(.subheadline).foregroundStyle(.secondary)
            }
            if !model.alertsEnabled { notificationPrompt }
            HStack { Text("Received text").font(.headline); Spacer(); Button { for c in model.computers { model.receive(c.computerID) } } label: { Label("Check now", systemImage: "arrow.clockwise").frame(minHeight: 44) }.font(.subheadline) }
            Text("Text stays on this device until ClipSend closes or you clear it.").font(.footnote).foregroundStyle(.secondary)
            if model.messages.isEmpty {
                VStack(spacing: 9) { Image(systemName: "tray").font(.title2).foregroundStyle(.secondary); Text("No text received").font(.subheadline.weight(.medium)); Text("ClipSend checks paired computers while this app is open.").font(.footnote).foregroundStyle(.secondary).multilineTextAlignment(.center) }.frame(maxWidth: .infinity).padding(.vertical, 30)
            } else {
                ForEach(model.messages) { message in
                    VStack(alignment: .leading, spacing: 12) {
                        HStack { VStack(alignment: .leading, spacing: 3) { Text(message.senderName).font(.subheadline.weight(.semibold)); Text(message.receivedAt, style: .time).font(.caption).foregroundStyle(.secondary) }; Spacer(); Button { model.clear(message) } label: { Image(systemName: "xmark.circle.fill").foregroundStyle(.tertiary).frame(minWidth: 44, minHeight: 44).contentShape(Rectangle()) }.accessibilityLabel("Clear received text") }
                        ScrollView { Text(message.text).font(.body).textSelection(.enabled).frame(maxWidth: .infinity, alignment: .leading).padding(.vertical, 2) }.frame(maxHeight: 240)
                        HStack(spacing: 10) {
                            Button { UIPasteboard.general.string = message.text; copiedID = message.id } label: { Label(copiedID == message.id ? "Copied" : "Copy", systemImage: copiedID == message.id ? "checkmark" : "doc.on.doc").frame(maxWidth: .infinity, minHeight: 44) }.buttonStyle(.borderedProminent)
                            if let url = safeLink(message.text) { Link(destination: url) { Label("Open link", systemImage: "arrow.up.right.square").frame(maxWidth: .infinity, minHeight: 44) }.buttonStyle(.bordered) }
                        }
                    }.padding(16).background(.background, in: RoundedRectangle(cornerRadius: 18)).accessibilityElement(children: .contain)
                }
            }
            Button { showScanner = true } label: { Label("Add another computer", systemImage: "plus") }.buttonStyle(.bordered).frame(maxWidth: .infinity)
        }.padding(18).background(.background, in: RoundedRectangle(cornerRadius: 22))
    }
    private var settingsView: some View {
        NavigationStack { List {
            Section("Paired computers") {
                ForEach(model.computers) { computer in
                    NavigationLink { ComputerSettings(computer: computer, model: model) } label: { Label(computer.name, systemImage: "desktopcomputer") }
                }
                if model.computers.isEmpty { Text("No computers paired yet.").foregroundStyle(.secondary) }
            }
            Section("Notifications") {
                if model.authorization == .notDetermined { Button("Enable notifications") { Task { await model.enableNotifications() } } }
                else { Button("Notification settings") { Task { await model.openSettings() } } }
                Text("Tap an alert to receive the text directly in ClipSend.").font(.footnote).foregroundStyle(.secondary)
            }
            Section("Local network") {
                Button("Check access") { model.checkLocalNetwork() }
                if model.localNetwork == .denied || model.localNetwork == .unavailable {
                    Button("Open Settings") { Task { await model.openAppSettings() } }
                }
                if model.localNetwork == .allowed { Label("Access confirmed", systemImage: "checkmark.circle") }
                if model.localNetwork == .checking { ProgressView("Checking access…") }
            }
            Section("This device") { TextField("Device name", text: $deviceName).textContentType(.name); Button("Refresh push registration") { model.refreshPushRegistration() }; if model.token == nil { Text("Push registration is required for pairing and alerts.").font(.footnote).foregroundStyle(.secondary) } }
            Section { DisclosureGroup("Technical details") { LabeledContent("App identifier", value: Bundle.main.bundleIdentifier ?? "Unavailable"); LabeledContent("Push environment", value: (try? BuildConfiguration.environment().rawValue) ?? "Unavailable") } }
        }.navigationTitle("Settings").navigationBarTitleDisplayMode(.inline).toolbar { ToolbarItem(placement: .topBarTrailing) { Button("Done") { showSettings = false } } } }
    }
    private func pairApprovalSheet(_ invitation: PairingInvitation) -> some View {
        NavigationStack { ScrollView { VStack(alignment: .leading, spacing: 16) {
            Label("Pair with \(invitation.computerName)", systemImage: "desktopcomputer").font(.title2.bold())
            Text("Approve the request on your computer to finish pairing.").foregroundStyle(.secondary)
            TextField("Name this device", text: $deviceName).textFieldStyle(.roundedBorder).textContentType(.name)
            Button { Task { await model.pair(deviceName: deviceName) } } label: { if model.pairing { ProgressView().frame(maxWidth: .infinity) } else { Text("Request pairing").frame(maxWidth: .infinity) } }.buttonStyle(.borderedProminent).controlSize(.large).disabled(model.pairing)
            if model.token == nil { Button("Retry push registration") { model.refreshPushRegistration() }.buttonStyle(.bordered) }
            notificationPrompt
            if !model.status.isEmpty { Text(model.status).font(.footnote).foregroundStyle(.secondary) }
            Spacer()
        }.padding(24) }.navigationTitle("Pair computer").navigationBarTitleDisplayMode(.inline).toolbar { ToolbarItem(placement: .topBarTrailing) { Button("Cancel") { model.pairInvitation = nil } } } }
    }
    private func safeLink(_ text: String) -> URL? {
        guard let url = URL(string: text.trimmingCharacters(in: .whitespacesAndNewlines)), ["https", "http"].contains(url.scheme?.lowercased() ?? ""), url.host != nil, url.user == nil, url.password == nil else { return nil }
        return url
    }
    @ViewBuilder private var notificationPrompt: some View {
        if model.authorization == .notDetermined {
            Button("Enable notifications") { Task { await model.enableNotifications() } }.buttonStyle(.bordered)
        } else if model.authorization == .denied {
            VStack(alignment: .leading, spacing: 8) {
                Text("Alerts are off. Open ClipSend to receive, or enable notifications in Settings.").font(.footnote)
                Button("Open Settings") { Task { await model.openSettings() } }.buttonStyle(.bordered)
            }
        }
    }
}

private struct InvitationSheet: Identifiable { let invitation: PairingInvitation; var id: String { invitation.computerID } }

private struct ComputerSettings: View {
    let computer: PairedComputer; @ObservedObject var model: CompanionModel; @Environment(\.dismiss) private var dismiss; @State private var name = ""
    var body: some View { Form { Section("Computer name") { TextField("Name", text: $name); Button("Save name") { if model.rename(computer, to: name) { dismiss() } }; if !model.status.isEmpty { Text(model.status).font(.footnote).foregroundStyle(.secondary) } }; Section { DisclosureGroup("Technical details") { LabeledContent("Computer identifier", value: computer.computerID); LabeledContent("Certificate fingerprint", value: computer.certificateSHA256) } }; Section { Button("Check for text") { model.receive(computer.computerID) }; Button("Forget computer", role: .destructive) { if model.forget(computer) { dismiss() } } } }.navigationTitle(computer.name).onAppear { name = computer.name } }
}
