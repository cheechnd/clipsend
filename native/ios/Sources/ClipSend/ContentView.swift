import SwiftUI
import UIKit
import PhotosUI
import QuickLook
import UniformTypeIdentifiers
import ImageIO

struct ContentView: View {
    @ObservedObject var model: CompanionModel
    @State private var showScanner = false
    @State private var scannedCode: String?
    @State private var showSettings = false
    @State private var pasted = ""
    @State private var showingPaste = false
    @State private var deviceName = UIDevice.current.name
    @State private var copiedID: String?
    @State private var outboundText = ""
    @State private var outboundFiles: [URL] = []
    @State private var showingPhotos = false
    @State private var selectedComputer = ""
    @State private var showingFiles = false
    @State private var previewFile: URL?
    @State private var previewImage: UIImage?
    @State private var shareItems: [Any] = []
    @State private var sendExpanded = false
    @State private var outboundDirectory: URL?
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
            .fileImporter(isPresented: $showingFiles, allowedContentTypes: [.item], allowsMultipleSelection: true) { result in
                do { let urls = try result.get(); for url in urls { outboundFiles.append(try stageFile(url)) } }
                catch { model.errorMessage = error.localizedDescription }
            }
            .sheet(isPresented: $showingPhotos) { PhotoLibraryPicker { providers in Task { for provider in providers { do { outboundFiles.append(try await stagePhoto(provider)) } catch { model.errorMessage = error.localizedDescription } } } } }
            .sheet(isPresented: Binding(get: { !shareItems.isEmpty }, set: { if !$0 { shareItems = [] } })) { ActivityShare(items: shareItems) }
            .sheet(isPresented: Binding(get: { previewFile != nil }, set: { if !$0 { previewFile = nil } })) {
                if let previewFile {
                    NavigationStack {
                        QuickLookFilePreview(url: previewFile)
                            .navigationTitle(previewFile.lastPathComponent)
                            .navigationBarTitleDisplayMode(.inline)
                            .toolbar { ToolbarItem(placement: .topBarTrailing) { Button("Done") { self.previewFile = nil } } }
                    }
                }
            }
            .sheet(isPresented: Binding(get: { previewImage != nil }, set: { if !$0 { previewImage = nil } })) { if let previewImage { NavigationStack { GeometryReader { geometry in Image(uiImage: previewImage).resizable().scaledToFit().frame(width: geometry.size.width, height: geometry.size.height) }.navigationTitle("Image preview").toolbar { ToolbarItem(placement: .topBarTrailing) { Button("Done") { self.previewImage = nil } } } } } }
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
            HStack { Text("Received content").font(.headline); Spacer(); Button { for c in model.computers { model.receive(c.computerID) } } label: { Label("Check now", systemImage: "arrow.clockwise").frame(minHeight: 44) }.font(.subheadline) }
            if let progress = model.receiveProgress { ProgressView(value: progress); Button("Cancel receive", role: .cancel) { model.cancelReceive() }.font(.footnote) }
            Text("Received items stay on this device until ClipSend closes or you clear them.").font(.footnote).foregroundStyle(.secondary)
            if model.messages.isEmpty {
                VStack(spacing: 9) { Image(systemName: "tray").font(.title2).foregroundStyle(.secondary); Text("Nothing received yet").font(.subheadline.weight(.medium)); Text("ClipSend checks paired computers while this app is open.").font(.footnote).foregroundStyle(.secondary).multilineTextAlignment(.center) }.frame(maxWidth: .infinity).padding(.vertical, 30)
            } else {
                ForEach(model.messages) { message in
                    VStack(alignment: .leading, spacing: 12) {
                        HStack { VStack(alignment: .leading, spacing: 3) { Text(message.senderName).font(.subheadline.weight(.semibold)); Text(message.receivedAt, style: .time).font(.caption).foregroundStyle(.secondary) }; Spacer(); Button { model.clear(message) } label: { Image(systemName: "xmark.circle.fill").foregroundStyle(.tertiary).frame(minWidth: 44, minHeight: 44).contentShape(Rectangle()) }.accessibilityLabel("Clear received text") }
                        if !message.text.isEmpty { ScrollView { Text(message.text).font(.body).textSelection(.enabled).frame(maxWidth: .infinity, alignment: .leading).padding(.vertical, 2) }.frame(maxHeight: 240) }
                        ForEach(message.attachments) { attachment in
                            HStack(spacing: 10) {
                                Image(systemName: attachment.descriptor.mime.hasPrefix("image/") ? "photo" : "doc")
                                VStack(alignment: .leading) { Text(attachment.descriptor.name).lineLimit(1); Text(ByteCountFormatter.string(fromByteCount: attachment.descriptor.size, countStyle: .file)).font(.caption).foregroundStyle(.secondary) }
                                Spacer()
                                Button("Preview") { showPreview(attachment) }.buttonStyle(.bordered)
                                Menu { Button("Share or Save…", systemImage: "square.and.arrow.up") { shareItems = [attachment.fileURL] }; if attachment.descriptor.mime.hasPrefix("image/") { Button("Copy image", systemImage: "doc.on.doc") { if copyOriginalImage(attachment) { copiedID = message.id } else { model.errorMessage = "This image format can’t be copied safely. Use Share or Save instead." } } } } label: { Image(systemName: "ellipsis.circle").frame(width: 44, height: 44) }.accessibilityLabel("Attachment actions")
                            }
                        }
                        if copiedID == message.id { Label("Copied", systemImage: "checkmark.circle.fill").font(.footnote).foregroundStyle(.green) }
                        HStack(spacing: 10) {
                            if !message.text.isEmpty { Button { UIPasteboard.general.string = message.text; copiedID = message.id } label: { Label(copiedID == message.id ? "Copied" : "Copy", systemImage: copiedID == message.id ? "checkmark" : "doc.on.doc").frame(maxWidth: .infinity, minHeight: 44) }.buttonStyle(.borderedProminent) }
                            if !message.attachments.isEmpty { Button { shareItems = message.attachments.map(\.fileURL) } label: { Label("Share / Save", systemImage: "square.and.arrow.up").frame(maxWidth: .infinity, minHeight: 44) }.buttonStyle(.bordered) }
                            if let url = safeLink(message.text) { Link(destination: url) { Label("Open link", systemImage: "arrow.up.right.square").frame(maxWidth: .infinity, minHeight: 44) }.buttonStyle(.bordered) }
                        }
                    }.padding(16).background(.background, in: RoundedRectangle(cornerRadius: 18)).accessibilityElement(children: .contain)
                }
            }
            outboundCard
            Button { showScanner = true } label: { Label("Add another computer", systemImage: "plus") }.buttonStyle(.bordered).frame(maxWidth: .infinity)
        }.padding(18).background(.background, in: RoundedRectangle(cornerRadius: 22))
    }
    private var outboundCard: some View {
        VStack(alignment: .leading, spacing: 12) {
          DisclosureGroup(isExpanded: $sendExpanded) {
            VStack(alignment: .leading, spacing: 12) {
            Picker("Computer", selection: $selectedComputer) {
                Text("Choose a computer").tag("")
                ForEach(model.computers) { Text($0.name).tag($0.computerID) }
            }.pickerStyle(.menu)
            TextField("Text to send", text: $outboundText, axis: .vertical).lineLimit(2...5).textFieldStyle(.roundedBorder)
            if !outboundFiles.isEmpty { ForEach(Array(outboundFiles.enumerated()), id: \.offset) { index, url in HStack { Image(systemName: "paperclip"); Text(url.lastPathComponent).lineLimit(1); Spacer(); Button { removeOutboundFile(at: index) } label: { Image(systemName: "xmark.circle.fill") }.accessibilityLabel("Remove \(url.lastPathComponent)") } } }
            HStack { Button { showingFiles = true } label: { Label("Files", systemImage: "folder") }.buttonStyle(.bordered); Button { showingPhotos = true } label: { Label("Photos", systemImage: "photo") }.buttonStyle(.bordered); Spacer(); Button("Paste") { if let value = UIPasteboard.general.string { outboundText = value } }.buttonStyle(.bordered) }
            Button { model.send(text: outboundText, files: outboundFiles, to: selectedComputer) } label: { Label("Send", systemImage: "arrow.up.circle.fill").frame(maxWidth: .infinity, minHeight: 44) }.buttonStyle(.borderedProminent).disabled(selectedComputer.isEmpty || (outboundText.isEmpty && outboundFiles.isEmpty) || model.transferProgress != nil)
            if let progress = model.transferProgress { ProgressView(value: progress); Button("Cancel transfer", role: .cancel) { model.cancelTransfer() }.buttonStyle(.bordered) }
            if let status = model.transferStatus { Text(status).font(.footnote).foregroundStyle(.secondary).accessibilityAddTraits(.updatesFrequently) }
            }
          } label: { Label("Send to a computer", systemImage: "arrow.up.right.circle").font(.headline) }
        }.padding(18).background(.background, in: RoundedRectangle(cornerRadius: 22))
    }
    private func stageFile(_ source: URL) throws -> URL {
        let scoped = source.startAccessingSecurityScopedResource(); defer { if scoped { source.stopAccessingSecurityScopedResource() } }
        guard let size = try? source.resourceValues(forKeys: [.fileSizeKey]).fileSize else { throw StagingFailure.unreadable }
        guard size <= 128 * 1024 * 1024 else { throw StagingFailure.tooLarge }
        guard outboundFiles.count < 16 else { throw StagingFailure.tooMany }
        guard outboundByteCount + Int64(size) <= 256 * 1024 * 1024 else { throw StagingFailure.aggregateLimit }
        let target = stageTarget(named: source.lastPathComponent)
        do { try FileManager.default.copyItem(at: source, to: target); return target } catch { throw StagingFailure.unreadable }
    }
    private func stagePhoto(_ provider: NSItemProvider) async throws -> URL {
        guard outboundFiles.count < 16 else { throw StagingFailure.tooMany }
        guard outboundByteCount < 256 * 1024 * 1024 else { throw StagingFailure.aggregateLimit }
        let remainingBytes = 256 * 1024 * 1024 - outboundByteCount
        let directory = stageDirectory()
        let suggestedName = SafeFilename.sanitize(provider.suggestedName ?? "photo")
        return try await withCheckedThrowingContinuation { (continuation: CheckedContinuation<URL, Error>) in
            provider.loadFileRepresentation(forTypeIdentifier: UTType.image.identifier) { url, _ in
                guard let url, let size = try? url.resourceValues(forKeys: [.fileSizeKey]).fileSize else { continuation.resume(throwing: StagingFailure.unreadable); return }
                guard size <= 128 * 1024 * 1024 else { continuation.resume(throwing: StagingFailure.tooLarge); return }
                guard Int64(size) <= remainingBytes else { continuation.resume(throwing: StagingFailure.aggregateLimit); return }
                let ext = url.pathExtension
                let name = (URL(fileURLWithPath: suggestedName).pathExtension.isEmpty && !ext.isEmpty) ? suggestedName + "." + ext : suggestedName
                let target = directory.appendingPathComponent(UUID().uuidString, isDirectory: true).appendingPathComponent(SafeFilename.sanitize(name))
                try? FileManager.default.createDirectory(at: target.deletingLastPathComponent(), withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
                do { try FileManager.default.copyItem(at: url, to: target); continuation.resume(returning: target) }
                catch { continuation.resume(throwing: StagingFailure.unreadable) }
            }
        }
    }
    private var outboundByteCount: Int64 { outboundFiles.reduce(0) { $0 + Int64((try? $1.resourceValues(forKeys: [.fileSizeKey]).fileSize) ?? 0) } }
    private func stageDirectory() -> URL {
        if let outboundDirectory { return outboundDirectory }
        let temp = FileManager.default.temporaryDirectory
        if let old = try? FileManager.default.contentsOfDirectory(at: temp, includingPropertiesForKeys: nil) { old.filter { $0.lastPathComponent.hasPrefix("ClipSend-Outbox-") }.forEach { try? FileManager.default.removeItem(at: $0) } }
        let dir = temp.appendingPathComponent("ClipSend-Outbox-" + UUID().uuidString, isDirectory: true)
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700]); outboundDirectory = dir; return dir
    }
    private func stageTarget(named name: String) -> URL {
        let dir = stageDirectory().appendingPathComponent(UUID().uuidString, isDirectory: true)
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        return dir.appendingPathComponent(SafeFilename.sanitize(name))
    }
    private func removeOutboundFile(at index: Int) {
        let url = outboundFiles.remove(at: index)
        if let outboundDirectory, url.deletingLastPathComponent().deletingLastPathComponent().standardizedFileURL == outboundDirectory.standardizedFileURL { try? FileManager.default.removeItem(at: url.deletingLastPathComponent()) }
        else { try? FileManager.default.removeItem(at: url) }
    }
    private func imageType(for mime: String) -> UTType? {
        switch mime.lowercased() { case "image/png": .png; case "image/jpeg", "image/jpg": .jpeg; case "image/heic": .heic; default: nil }
    }
    private func safeImage(_ url: URL, mime: String) -> UIImage? {
        guard let expectedType = imageType(for: mime), let source = CGImageSourceCreateWithURL(url as CFURL, nil),
              let sourceType = CGImageSourceGetType(source) as String?, sourceType == expectedType.identifier,
              let properties = CGImageSourceCopyPropertiesAtIndex(source, 0, nil) as? [CFString: Any],
              let width = properties[kCGImagePropertyPixelWidth] as? Int, let height = properties[kCGImagePropertyPixelHeight] as? Int,
              width > 0, height > 0, let image = CGImageSourceCreateThumbnailAtIndex(source, 0, [kCGImageSourceCreateThumbnailFromImageAlways: true, kCGImageSourceCreateThumbnailWithTransform: true, kCGImageSourceThumbnailMaxPixelSize: 2048] as CFDictionary) else { return nil }
        return UIImage(cgImage: image)
    }
    private func copyOriginalImage(_ attachment: ReceivedAttachment) -> Bool {
        guard let type = imageType(for: attachment.descriptor.mime), safeImage(attachment.fileURL, mime: attachment.descriptor.mime) != nil,
              let values = try? attachment.fileURL.resourceValues(forKeys: [.fileSizeKey]), let size = values.fileSize,
              size <= 128 * 1024 * 1024, let data = try? Data(contentsOf: attachment.fileURL, options: [.mappedIfSafe]), data.count == size else { return false }
        UIPasteboard.general.setData(data, forPasteboardType: type.identifier)
        return true
    }
    private func showPreview(_ attachment: ReceivedAttachment) {
        if attachment.descriptor.mime.hasPrefix("image/"), let image = safeImage(attachment.fileURL, mime: attachment.descriptor.mime) { previewImage = image }
        else { previewFile = attachment.fileURL }
    }
    private enum StagingFailure: LocalizedError {
        case tooLarge, tooMany, aggregateLimit, unreadable
        var errorDescription: String? {
            switch self {
            case .tooLarge: "A file exceeds the 128 MiB per item limit."
            case .tooMany: "Choose no more than 16 files."
            case .aggregateLimit: "The selected files exceed the 256 MiB total limit."
            case .unreadable: "ClipSend couldn’t read that item. Try selecting it again."
            }
        }
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

private struct ActivityShare: UIViewControllerRepresentable {
    let items: [Any]
    func makeUIViewController(context: Context) -> UIActivityViewController { UIActivityViewController(activityItems: items, applicationActivities: nil) }
    func updateUIViewController(_ controller: UIActivityViewController, context: Context) { }
}

private struct PhotoLibraryPicker: UIViewControllerRepresentable {
    let selected: ([NSItemProvider]) -> Void
    func makeCoordinator() -> Coordinator { Coordinator(selected: selected) }
    func makeUIViewController(context: Context) -> PHPickerViewController {
        var config = PHPickerConfiguration(photoLibrary: .shared()); config.filter = .images; config.selectionLimit = 16
        let picker = PHPickerViewController(configuration: config); picker.delegate = context.coordinator; return picker
    }
    func updateUIViewController(_ picker: PHPickerViewController, context: Context) { }
    final class Coordinator: NSObject, PHPickerViewControllerDelegate {
        let selected: ([NSItemProvider]) -> Void
        init(selected: @escaping ([NSItemProvider]) -> Void) { self.selected = selected }
        func picker(_ picker: PHPickerViewController, didFinishPicking results: [PHPickerResult]) { picker.dismiss(animated: true); selected(results.map(\.itemProvider)) }
    }
}

private struct QuickLookFilePreview: UIViewControllerRepresentable {
    let url: URL
    func makeCoordinator() -> Coordinator { Coordinator(url: url) }
    func makeUIViewController(context: Context) -> QLPreviewController { let controller = QLPreviewController(); controller.dataSource = context.coordinator; return controller }
    func updateUIViewController(_ controller: QLPreviewController, context: Context) { context.coordinator.url = url; controller.reloadData() }
    final class Coordinator: NSObject, QLPreviewControllerDataSource {
        var url: URL
        init(url: URL) { self.url = url }
        func numberOfPreviewItems(in controller: QLPreviewController) -> Int { 1 }
        func previewController(_ controller: QLPreviewController, previewItemAt index: Int) -> QLPreviewItem { url as NSURL }
    }
}

private struct ComputerSettings: View {
    let computer: PairedComputer; @ObservedObject var model: CompanionModel; @Environment(\.dismiss) private var dismiss; @State private var name = ""
    var body: some View { Form { Section("Computer name") { TextField("Name", text: $name); Button("Save name") { if model.rename(computer, to: name) { dismiss() } }; if !model.status.isEmpty { Text(model.status).font(.footnote).foregroundStyle(.secondary) } }; Section { DisclosureGroup("Technical details") { LabeledContent("Computer identifier", value: computer.computerID); LabeledContent("Certificate fingerprint", value: computer.certificateSHA256) } }; Section { Button("Check for text") { model.receive(computer.computerID) }; Button("Forget computer", role: .destructive) { if model.forget(computer) { dismiss() } } } }.navigationTitle(computer.name).onAppear { name = computer.name } }
}
