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
    @State private var pendingPairingMethod: PairingMethod?
    private enum PairingMethod { case scan, paste }
    @State private var pasted = ""
    @State private var showingPaste = false
    @State private var deviceName = UIDevice.current.name
    @State private var copiedID: String?
    @State private var expandedMessages: Set<String> = []
    @State private var outboundText = ""
    @State private var outboundFiles: [URL] = []
    @State private var showingPhotos = false
    @State private var selectedComputer = ""
    @State private var showingFiles = false
    @State private var previewFile: URL?
    @State private var previewImage: UIImage?
    @State private var shareItems: [Any] = []
    @State private var section = HomeSection.inbox
    @State private var stagingPhotos = false
    @State private var readingClipboard = false
    @State private var composing = false
    @State private var importError: String?
    @State private var inboxScrollRequest = UUID()
    @State private var awaitingNotificationItem = false
    @State private var submittedText: String?
    @State private var submittedFiles: [URL] = []
    @FocusState private var editingText: Bool
    @AccessibilityFocusState private var undoFocused: Bool
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize
    private enum HomeSection: String, CaseIterable { case inbox = "Inbox", send = "Send" }
    @State private var outboundDirectory: URL?
    private var width: CGFloat { 640 }

    var body: some View {
        NavigationStack {
            homeContent
            .background(ClipSendStyle.canvas)
            .safeAreaInset(edge: .bottom, spacing: 0) { undoRow }
            .toolbar(.hidden, for: .navigationBar)
            .sheet(isPresented: $showScanner, onDismiss: { if let code = scannedCode { scannedCode = nil; model.acceptInvitation(code) } }) {
                NavigationStack {
                    QRScanner { scannedCode = $0; showScanner = false }
                        .ignoresSafeArea(edges: .bottom)
                        .navigationTitle("Scan pairing code")
                        .navigationBarTitleDisplayMode(.inline).toolbar(.visible, for: .navigationBar)
                        .toolbar { ToolbarItem(placement: .cancellationAction) { Button("Cancel") { showScanner = false } } }
                }
            }
            .sheet(isPresented: $showSettings, onDismiss: {
                if let method = pendingPairingMethod {
                    pendingPairingMethod = nil
                    switch method { case .scan: showScanner = true; case .paste: showingPaste = true }
                }
            }) { settingsView }
            .onChange(of: model.completedSendID) { _, completed in
                guard completed != nil, let submittedText else { return }
                if outboundText == submittedText { outboundText = "" }
                for url in submittedFiles { if let index = outboundFiles.firstIndex(of: url) { removeOutboundFile(at: index) } }
                self.submittedText = nil; submittedFiles = []; importError = nil
            }
            .onChange(of: model.openedNotificationID) { _, opened in
                guard opened != nil else { return }
                section = .inbox; editingText = false
                awaitingNotificationItem = true; inboxScrollRequest = UUID()
                previewFile = nil; previewImage = nil; shareItems = []
                // Preserve an in-progress pairing flow and the send draft.
                if pendingPairingMethod == nil { showSettings = false }
            }
            .onAppear { reconcileComputer() }
            .onChange(of: model.computers.map(\.computerID)) { _, _ in reconcileComputer() }
            .toolbar { ToolbarItemGroup(placement: .keyboard) { Spacer(); Button("Done") { editingText = false } } }
            .alert("Paste pairing code", isPresented: $showingPaste) {
                TextField("Pairing JSON", text: $pasted, axis: .vertical).lineLimit(3...6)
                Button("Continue") { model.acceptInvitation(pasted) }
                Button("Cancel", role: .cancel) { }
            } message: { Text("Paste the QR code contents from your computer.") }
            .sheet(item: Binding(get: { model.pairInvitation.map(InvitationSheet.init) }, set: { if $0 == nil { model.pairInvitation = nil } })) { item in
                pairApprovalSheet(item.invitation).presentationDetents([.medium, .large])
            }
            .fileImporter(isPresented: $showingFiles, allowedContentTypes: [.item], allowsMultipleSelection: true) { result in
                do {
                    let urls = try result.get()
                    guard !urls.isEmpty else { return }
                    retireTransferResult()
                    for url in urls { outboundFiles.append(try stageFile(url)) }
                    importError = nil
                } catch {
                    if (error as NSError).code != NSUserCancelledError { importError = error.localizedDescription }
                }
            }
            .sheet(isPresented: $showingPhotos) {
                PhotoLibraryPicker { providers in
                    guard !providers.isEmpty else { return }
                    stagingPhotos = true; retireTransferResult()
                    Task {
                        defer { stagingPhotos = false }
                        var preparationError: String?
                        for provider in providers {
                            do { outboundFiles.append(try await stagePhoto(provider)) }
                            catch { if preparationError == nil { preparationError = error.localizedDescription } }
                        }
                        // A later successful photo must not hide a failure in this same selection.
                        importError = preparationError
                    }
                }
            }
            .sheet(isPresented: Binding(get: { !shareItems.isEmpty }, set: { if !$0 { shareItems = [] } })) { ActivityShare(items: shareItems) }
            .sheet(isPresented: Binding(get: { previewFile != nil }, set: { if !$0 { previewFile = nil } })) {
                if let previewFile {
                    NavigationStack {
                        QuickLookFilePreview(url: previewFile)
                            .navigationTitle(previewFile.lastPathComponent)
                            .navigationBarTitleDisplayMode(.inline).toolbar(.visible, for: .navigationBar)
                            .toolbar { ToolbarItem(placement: .topBarTrailing) { Button("Done") { self.previewFile = nil } } }
                    }
                }
            }
            .sheet(isPresented: Binding(get: { previewImage != nil }, set: { if !$0 { previewImage = nil } })) { if let previewImage { NavigationStack { GeometryReader { geometry in Image(uiImage: previewImage).resizable().scaledToFit().frame(width: geometry.size.width, height: geometry.size.height) }.navigationTitle("Image preview").toolbar(.visible, for: .navigationBar).toolbar { ToolbarItem(placement: .topBarTrailing) { Button("Done") { self.previewImage = nil } } } } } }
        }.tint(ClipSendStyle.ink)
    }
    @ViewBuilder private var undoRow: some View {
        if let pending = model.pendingClear {
            HStack(spacing: 16) {
                Text("Cleared").font(.subheadline.weight(.medium))
                    .frame(maxWidth: .infinity, alignment: .leading)
                Button("Undo", action: undoClear)
                    .font(.subheadline.weight(.semibold)).frame(minWidth: 60, minHeight: 44)
                    .accessibilityLabel("Cleared. Undo")
                    .accessibilityHint("Restores all recently cleared content. Available for eight seconds after the last clear.")
                    .accessibilityFocused($undoFocused)
            }
            .padding(.horizontal, 16).padding(.vertical, 8)
            .background(ClipSendStyle.surface, in: RoundedRectangle(cornerRadius: 16))
            .padding(.horizontal, 20).padding(.vertical, 8).frame(maxWidth: width)
            .frame(maxWidth: .infinity).background(ClipSendStyle.canvas)
            .id(pending.id)
            .onAppear { undoFocused = true }
        }
    }
    private func clearDraft() {
        editingText = false
        model.clearDraft(text: outboundText, files: outboundFiles)
        outboundText = ""; outboundFiles = []; importError = nil
        submittedText = nil; submittedFiles = []
        retireTransferResult()
    }
    private func undoClear() {
        guard let contents = model.undoClear() else { return }
        var restoredText: [String] = []
        var restoredFiles: [URL] = []
        for content in contents {
            switch content {
            case .received:
                section = .inbox; inboxScrollRequest = UUID()
            case .draft(let text, let files):
                restoredText.append(text); restoredFiles.append(contentsOf: files)
                retireTransferResult(); section = .send; composing = true
            }
        }
        if !restoredText.isEmpty {
            // Preserve anything written/imported since Clear, including a new send.
            outboundText = (restoredText + [outboundText]).filter { !$0.isEmpty }.joined(separator: "\n")
            outboundFiles = restoredFiles + outboundFiles
        }
    }
    private var homeContent: some View {
        ScrollViewReader { scroll in
            VStack(spacing: 0) {
                VStack(spacing: 20) {
                    HStack(spacing: 11) {
                        ClipSendMark()
                        Text("ClipSend").font(.title3.weight(.semibold))
                        Spacer()
                        Button { showSettings = true } label: {
                            Image(systemName: "gearshape").font(.title3)
                                .frame(width: 44, height: 44)
                        }.buttonStyle(.plain).foregroundStyle(.secondary).accessibilityLabel("Settings")
                    }
                    if !model.computers.isEmpty {
                        let navigationLayout = dynamicTypeSize.isAccessibilitySize
                            ? AnyLayout(VStackLayout(spacing: 4)) : AnyLayout(HStackLayout(spacing: 4))
                        navigationLayout {
                            sectionButton(.inbox, icon: "tray", count: model.messages.count)
                            sectionButton(.send, icon: "arrow.up.right")
                        }
                        .padding(4)
                        .background(ClipSendStyle.ink.opacity(0.055), in: RoundedRectangle(cornerRadius: 15))
                    }
                }
                .padding(.horizontal, 20).padding(.top, 12).padding(.bottom, 20)
                .frame(maxWidth: width)
                ScrollView {
                    LazyVStack(alignment: .leading, spacing: 18) {
                        Color.clear.frame(height: 0).id("content-top")
                        if model.computers.isEmpty { onboarding } else { pairedContent }
                        if let status = visibleStatus { Text(status).font(.footnote).foregroundStyle(.secondary).frame(maxWidth: .infinity, alignment: .leading) }
                        if let error = model.errorMessage { Text(error).font(.footnote).foregroundStyle(.red).accessibilityAddTraits(.updatesFrequently) }
                    }
                    .padding(.horizontal, 20).padding(.bottom, 16)
                    .frame(maxWidth: width, alignment: .leading).frame(maxWidth: .infinity)
                }
                .scrollDismissesKeyboard(.interactively)
            }
            .onChange(of: section) { _, section in
                editingText = false
                if section == .send { awaitingNotificationItem = false }
                scroll.scrollTo("content-top", anchor: .top)
            }
            .onChange(of: inboxScrollRequest) { _, _ in
                scroll.scrollTo("content-top", anchor: .top)
            }
            .onChange(of: model.messages.map(\.id)) { old, new in
                guard section == .inbox, awaitingNotificationItem, !Set(new).subtracting(old).isEmpty else { return }
                awaitingNotificationItem = false
                // The tapped notification may precede its download; reveal the item when it arrives.
                scroll.scrollTo("content-top", anchor: .top)
            }
        }
    }
    private func sectionButton(_ destination: HomeSection, icon: String, count: Int = 0) -> some View {
        let selected = section == destination
        return Button { section = destination } label: {
            HStack(spacing: 8) {
                Image(systemName: icon)
                Text(destination.rawValue)
                if count > 0 {
                    Text("\(count)").font(.caption.weight(.semibold)).monospacedDigit()
                        .padding(.horizontal, 6).padding(.vertical, 2)
                        .background(ClipSendStyle.wash, in: Capsule())
                }
            }
            .font(.subheadline.weight(.semibold)).frame(maxWidth: .infinity, minHeight: 44)
            .foregroundStyle(selected ? ClipSendStyle.ink : .secondary)
            .background(selected ? ClipSendStyle.surface : .clear, in: RoundedRectangle(cornerRadius: 11))
        }
        .buttonStyle(.plain)
        .accessibilityLabel(destination == .inbox ? "Inbox, \(count) items" : "Send")
        .accessibilityAddTraits(selected ? .isSelected : [])
    }
    // Only user edits retire a transfer result. Success-driven cleanup preserves its confirmation.
    private func retireTransferResult() {
        if model.transferProgress == nil { model.transferStatus = nil }
    }
    private var draftText: Binding<String> {
        Binding(get: { outboundText }, set: { value in
            if value != outboundText { retireTransferResult(); outboundText = value }
        })
    }
    private var draftComputer: Binding<String> {
        Binding(get: { selectedComputer }, set: { value in
            if value != selectedComputer { retireTransferResult(); selectedComputer = value }
        })
    }
    // Routine polling should not compete with received content. Keep recovery messages visible.
    private var visibleStatus: String? {
        let status = model.status
        let routine = ["No active sends from ", "Received files from ", "Received text from ", "Paired with ", "Ready to receive when ", "Pair a computer to receive text."]
        return status.isEmpty || status == model.errorMessage || routine.contains(where: { status.hasPrefix($0) }) ? nil : status
    }
    private func reconcileComputer() {
        if !model.computers.contains(where: { $0.computerID == selectedComputer }) {
            retireTransferResult()
            selectedComputer = model.computers.count == 1 ? model.computers[0].computerID : ""
        }
    }
    private func markCopied(_ id: String) {
        copiedID = id
        Task { try? await Task.sleep(for: .seconds(2)); if copiedID == id { copiedID = nil } }
    }
    private var onboarding: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text(model.setupReady ? "Connect your computer" : "Set up ClipSend").font(.headline)
            if !model.setupStarted && !model.setupReady {
                Text("Send text, photos, and files between this device and your computer. Start by allowing notifications and local-network access.").foregroundStyle(.secondary)
                Button("Set up ClipSend") { Task { await model.startSetup() } }
                    .buttonStyle(.borderedProminent).controlSize(.large).disabled(model.preparingSetup)
            } else if !model.alertsEnabled && !model.skipAlerts {
                Label("Notifications", systemImage: "bell.badge").font(.subheadline.bold())
                Text("Get an alert when your computer sends something.").foregroundStyle(.secondary)
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
                Text("On your computer, open Devices in ClipSend and choose Pair a device. Scan its QR code, then approve the request there.").foregroundStyle(.secondary)
                DisclosureGroup("Using the command line?") {
                    Text("Run clipsend pair iphone on your computer, or clipsend pair iphone --code to get a code you can paste here.")
                        .font(.footnote).foregroundStyle(.secondary).textSelection(.enabled).padding(.top, 6)
                }.font(.subheadline)
                Button { showScanner = true } label: { Label("Scan QR code", systemImage: "qrcode.viewfinder") }.buttonStyle(.borderedProminent).controlSize(.large)
                Button("Paste pairing code") { showingPaste = true }.buttonStyle(.bordered)
            }
        }.padding(20).frame(maxWidth: .infinity, alignment: .leading)
            .background(ClipSendStyle.surface, in: RoundedRectangle(cornerRadius: 20))
    }
    private var pairedContent: some View {
        VStack(alignment: .leading, spacing: 20) {
            if model.justPaired && model.messages.isEmpty {
                Text("Computer paired. Send an item from its ClipSend panel to try it out.").font(.subheadline).foregroundStyle(.secondary)
            }
            if model.localNetwork == .denied || model.localNetwork == .unavailable {
                VStack(alignment: .leading, spacing: 8) {
                    Text("Allow Local Network access and connect both devices to the same Wi-Fi.").font(.subheadline)
                    Button("Network settings") { Task { await model.openAppSettings() } }.frame(minHeight: 44)
                }
            }
            if section == .inbox { inbox } else { outboundCard }
        }
    }
    private var inbox: some View {
        LazyVStack(alignment: .leading, spacing: 16) {
            HStack(spacing: 16) {
                Text("Received").font(.headline)
                Spacer()
                Button("Clear all") { model.clearAll(); expandedMessages.removeAll() }
                    .font(.subheadline)
                    .foregroundStyle(model.messages.isEmpty ? Color.secondary : ClipSendStyle.ink)
                    .frame(minHeight: 44)
                    .disabled(model.messages.isEmpty)
                Button { for computer in model.computers { model.receive(computer.computerID) } } label: {
                    Image(systemName: "arrow.clockwise").font(.body.weight(.medium)).frame(width: 44, height: 44)
                }.accessibilityLabel("Check for new items").disabled(model.receiveProgress != nil)
            }
            if let progress = model.receiveProgress {
                ProgressView("Receiving…", value: progress)
                Button("Cancel receive", role: .cancel) { model.cancelReceive() }.frame(minHeight: 44)
            }
            if model.messages.isEmpty {
                VStack(alignment: .leading, spacing: 20) {
                    Image(systemName: "tray.and.arrow.down")
                        .font(.system(size: 26, weight: .regular)).foregroundStyle(ClipSendStyle.ink)
                        .frame(width: 56, height: 56)
                        .background(ClipSendStyle.wash, in: RoundedRectangle(cornerRadius: 16))
                        .accessibilityHidden(true)
                    VStack(alignment: .leading, spacing: 8) {
                        Text("Ready to receive").font(.title3.weight(.semibold))
                        Text("Send text, photos, or files from ClipSend on your computer. Keep both devices on the same Wi-Fi.")
                            .font(.subheadline).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                    }
                    notificationPrompt
                }
                .padding(24).frame(maxWidth: .infinity, alignment: .leading)
                .background(ClipSendStyle.surface, in: RoundedRectangle(cornerRadius: 20))
            } else {
                ForEach(model.messages) { message in receivedCard(message) }
                Text("Temporary inbox. Save items you want to keep.").font(.footnote).foregroundStyle(.secondary)
                    .padding(.horizontal, 4)
            }
        }
    }
    private func receivedCard(_ message: ReceivedText) -> some View {
        VStack(alignment: .leading, spacing: 16) {
            HStack(alignment: .center, spacing: 10) {
                Image(systemName: "desktopcomputer").font(.subheadline)
                    .foregroundStyle(ClipSendStyle.ink).frame(width: 36, height: 36)
                    .background(ClipSendStyle.wash, in: RoundedRectangle(cornerRadius: 10)).accessibilityHidden(true)
                VStack(alignment: .leading, spacing: 4) {
                    Text(message.senderName).font(.subheadline.weight(.semibold))
                    Text(message.receivedAt, style: .time).font(.caption).foregroundStyle(.secondary)
                }
                Spacer(minLength: 8)
                Button { model.clear(message); expandedMessages.remove(message.id) } label: { Image(systemName: "xmark").frame(minWidth: 44, minHeight: 44) }
                    .foregroundStyle(.secondary).accessibilityLabel("Clear item from \(message.senderName)")
            }
            if !message.text.isEmpty {
                let canExpand = message.text.count > 300 || message.text.filter { $0 == "\n" }.count > 5
                Text(message.text).textSelection(.enabled)
                    .lineLimit(canExpand && !expandedMessages.contains(message.id) ? 8 : nil)
                    .frame(maxWidth: .infinity, alignment: .leading)
                if canExpand {
                    Button(expandedMessages.contains(message.id) ? "Show less" : "Show full text") {
                        if expandedMessages.contains(message.id) { expandedMessages.remove(message.id) } else { expandedMessages.insert(message.id) }
                    }.font(.subheadline).frame(minHeight: 44)
                }
                ViewThatFits(in: .horizontal) {
                    textActions(message, vertical: false)
                    textActions(message, vertical: true)
                }
            }
            ForEach(message.attachments) { attachment in
                Divider()
                attachmentView(attachment, messageID: message.id)
            }
            if message.attachments.count > 1 {
                Divider()
                Button { shareItems = message.attachments.map(\.fileURL) } label: { Label("Share all files", systemImage: "square.and.arrow.up").font(.subheadline.weight(.medium)).frame(minHeight: 44) }
            }
        }.padding(20).background(ClipSendStyle.surface, in: RoundedRectangle(cornerRadius: 20)).accessibilityElement(children: .contain)
    }
    private func textActions(_ message: ReceivedText, vertical: Bool) -> some View {
        let layout = vertical ? AnyLayout(VStackLayout(alignment: .leading, spacing: 4)) : AnyLayout(HStackLayout(spacing: 8))
        return layout {
                    Button { UIPasteboard.general.string = message.text; markCopied(message.id) } label: {
                        Label(copiedID == message.id ? "Copied" : "Copy text", systemImage: copiedID == message.id ? "checkmark" : "doc.on.doc").frame(minHeight: 32)
                    }.buttonStyle(ClipSendActionStyle())
                    if let url = safeLink(message.text) {
                        Link(destination: url) { Label("Open link", systemImage: "arrow.up.right.square").frame(minHeight: 44) }
                            .font(.subheadline.weight(.medium)).padding(.horizontal, 8)
                    }
        }.fixedSize(horizontal: !vertical, vertical: true)
    }
    private func attachmentView(_ attachment: ReceivedAttachment, messageID: String) -> some View {
        let isImage = attachment.descriptor.mime.hasPrefix("image/")
        return VStack(alignment: .leading, spacing: 10) {
            if isImage {
                Button { showPreview(attachment) } label: {
                    ClipSendThumbnail(url: attachment.fileURL, mime: attachment.descriptor.mime)
                }.buttonStyle(.plain).accessibilityLabel("Preview \(attachment.descriptor.name)")
            }
            HStack(spacing: 10) {
                if !isImage {
                    Image(systemName: attachment.descriptor.mime == "application/pdf" ? "doc.richtext" : "doc.text")
                        .font(.title2).foregroundStyle(ClipSendStyle.ink).frame(width: 36).accessibilityHidden(true)
                }
                VStack(alignment: .leading, spacing: 3) {
                    Text(attachment.descriptor.name).font(.subheadline.weight(.medium)).fixedSize(horizontal: false, vertical: true)
                    Text(ByteCountFormatter.string(fromByteCount: attachment.descriptor.size, countStyle: .file)).font(.caption).foregroundStyle(.secondary)
                }
            }
            ViewThatFits(in: .horizontal) {
                attachmentActions(attachment, messageID: messageID, vertical: false)
                attachmentActions(attachment, messageID: messageID, vertical: true)
            }
        }
    }
    private func attachmentActions(_ attachment: ReceivedAttachment, messageID: String, vertical: Bool) -> some View {
        let copyID = messageID + ":" + attachment.id
        let layout = vertical ? AnyLayout(VStackLayout(alignment: .leading, spacing: 0)) : AnyLayout(HStackLayout(spacing: 16))
        return layout {
            Button { showPreview(attachment) } label: { Label("Preview", systemImage: "eye").frame(minHeight: 44) }
            if imageType(for: attachment.descriptor.mime) != nil {
                Button {
                    if copyOriginalImage(attachment) { markCopied(copyID) }
                    else { model.errorMessage = "This image format can’t be copied safely. Use Share instead." }
                } label: { Label(copiedID == copyID ? "Copied" : "Copy image", systemImage: copiedID == copyID ? "checkmark" : "doc.on.doc").frame(minHeight: 44) }
            }
            Button { shareItems = [attachment.fileURL] } label: { Label("Share / Save", systemImage: "square.and.arrow.up").frame(minHeight: 44) }
        }.font(.subheadline.weight(.medium)).buttonStyle(.plain).foregroundStyle(ClipSendStyle.ink)
            .fixedSize(horizontal: !vertical, vertical: true)
    }
    private var outboundCard: some View {
        VStack(alignment: .leading, spacing: 16) {
            HStack(spacing: 12) {
                Image(systemName: "desktopcomputer").foregroundStyle(ClipSendStyle.ink).accessibilityHidden(true)
                Text("To").font(.subheadline).foregroundStyle(.secondary)
                Picker("To", selection: draftComputer) {
                    Text("Choose a computer").tag("")
                    ForEach(model.computers) { Text($0.name).tag($0.computerID) }
                }.pickerStyle(.menu).frame(maxWidth: .infinity, minHeight: 44, alignment: .leading)
                    .labelsHidden()
            }.padding(.horizontal, 16).padding(.vertical, 8)

            .disabled(model.transferProgress != nil || readingClipboard)
            Button(action: pasteAndSend) {
                Label("Paste & Send", systemImage: "document.on.clipboard").frame(maxWidth: .infinity)
            }.buttonStyle(ClipSendActionStyle(prominent: true))
                .disabled(selectedComputer.isEmpty || model.transferProgress != nil || stagingPhotos || readingClipboard)
            if readingClipboard { ProgressView("Reading clipboard…") }
            DisclosureGroup(isExpanded: $composing) {
                VStack(alignment: .leading, spacing: 16) {
                    VStack(alignment: .leading, spacing: 0) {
                        TextField("Write or paste text", text: draftText, axis: .vertical)
                            .lineLimit(4...10).focused($editingText).padding(20)
                            .accessibilityLabel("Text to send")
                        if outboundText.utf8.count > 64_000 { Text("Text is too long. Shorten it before sending.").font(.footnote).foregroundStyle(.red).padding(.horizontal, 20) }
                        if !outboundFiles.isEmpty {
                            VStack(spacing: 12) {
                                ForEach(Array(outboundFiles.enumerated()), id: \.element) { index, url in
                                    HStack(spacing: 12) {
                                        ClipSendThumbnail(url: url, compact: true)
                                        VStack(alignment: .leading, spacing: 3) {
                                            Text(url.lastPathComponent).font(.subheadline.weight(.medium)).fixedSize(horizontal: false, vertical: true)
                                            if let size = try? url.resourceValues(forKeys: [.fileSizeKey]).fileSize {
                                                Text(ByteCountFormatter.string(fromByteCount: Int64(size), countStyle: .file)).font(.caption).foregroundStyle(.secondary)
                                            }
                                        }
                                        Spacer(minLength: 0)
                                        Button { retireTransferResult(); removeOutboundFile(at: index) } label: { Image(systemName: "xmark").frame(width: 44, height: 44) }
                                            .foregroundStyle(.secondary).accessibilityLabel("Remove \(url.lastPathComponent)")
                                    }
                                }
                            }.padding(.horizontal, 20).padding(.bottom, 16)
                        }
                        Divider().padding(.horizontal, 16)
                        ViewThatFits(in: .horizontal) {
                            composerTools(vertical: false)
                            composerTools(vertical: true)
                        }.padding(.horizontal, 16).padding(.vertical, 6)
                    }
                    .background(ClipSendStyle.surface, in: RoundedRectangle(cornerRadius: 20))
                    .disabled(model.transferProgress != nil || stagingPhotos || readingClipboard)
                    if stagingPhotos { ProgressView("Adding photos…") }
                    if let importError { Text(importError).font(.footnote).foregroundStyle(.red).accessibilityAddTraits(.updatesFrequently) }
                    HStack(alignment: .center) {
                        if !outboundText.isEmpty || !outboundFiles.isEmpty || importError != nil {
                            Button("Clear draft", role: .destructive, action: clearDraft)
                                .font(.subheadline).frame(minHeight: 44).disabled(model.transferProgress != nil || stagingPhotos || readingClipboard)
                        }
                        Spacer(minLength: 12)
                        Button { editingText = false; importError = nil; submittedText = outboundText; submittedFiles = outboundFiles; model.send(text: outboundText, files: outboundFiles, to: selectedComputer) } label: {
                            Label("Send", systemImage: "arrow.up.right").frame(minWidth: 88)
                        }.buttonStyle(ClipSendActionStyle(prominent: true))
                            .disabled(selectedComputer.isEmpty || (outboundText.isEmpty && outboundFiles.isEmpty) || outboundText.utf8.count > 64_000 || model.transferProgress != nil || stagingPhotos || readingClipboard)
                    }

                }
            } label: {
                Label("Write or add files", systemImage: "square.and.pencil").font(.subheadline.weight(.medium)).frame(minHeight: 44)
            }
            if let progress = model.transferProgress {
                ProgressView("Sending…", value: progress)
                Button("Cancel send", role: .cancel) { model.cancelTransfer() }.frame(minHeight: 44)
            }
            if let status = model.transferStatus { Text(status).font(.subheadline).foregroundStyle(.secondary).accessibilityAddTraits(.updatesFrequently) }
            Text("Turn on Receive in ClipSend on your computer.")
                .font(.footnote).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
        }
    }

    private func pasteAndSend() {
        guard !selectedComputer.isEmpty, model.transferProgress == nil, !readingClipboard else { return }
        editingText = false
        readingClipboard = true
        retireTransferResult()
        let destination = selectedComputer
        Task {
            defer { readingClipboard = false }
            do {
                let payload = try await ClipboardSend.read()
                // A quick send never replaces or clears an unfinished composer draft.
                submittedText = nil; submittedFiles = []
                model.send(text: payload.text, files: payload.files, to: destination) {
                    payload.removeTemporaryFiles()
                }
            } catch { model.transferStatus = error.localizedDescription }
        }
    }
    private func composerTools(vertical: Bool) -> some View {
        let layout = vertical ? AnyLayout(VStackLayout(alignment: .leading, spacing: 0)) : AnyLayout(HStackLayout(spacing: 20))
        return layout {
            Button { editingText = false; showingFiles = true } label: { Label("Files", systemImage: "folder").frame(minHeight: 44) }
            Button { editingText = false; showingPhotos = true } label: { Label("Photos", systemImage: "photo").frame(minHeight: 44) }
        }.font(.subheadline.weight(.medium)).buttonStyle(.plain).foregroundStyle(ClipSendStyle.ink)
            .fixedSize(horizontal: !vertical, vertical: true)
    }
    private func stageFile(_ source: URL) throws -> URL {
        let scoped = source.startAccessingSecurityScopedResource(); defer { if scoped { source.stopAccessingSecurityScopedResource() } }
        guard let size = try? source.resourceValues(forKeys: [.fileSizeKey]).fileSize else { throw StagingFailure.unreadable }
        guard size <= 128 * 1024 * 1024 else { throw StagingFailure.tooLarge }
        guard retainedOutboundFiles.count < 16 else { throw StagingFailure.tooMany }
        guard outboundByteCount + Int64(size) <= 256 * 1024 * 1024 else { throw StagingFailure.aggregateLimit }
        let target = stageTarget(named: source.lastPathComponent)
        do { try FileManager.default.copyItem(at: source, to: target); return target } catch { throw StagingFailure.unreadable }
    }
    private func stagePhoto(_ provider: NSItemProvider) async throws -> URL {
        guard retainedOutboundFiles.count < 16 else { throw StagingFailure.tooMany }
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
    private var retainedOutboundFiles: [URL] { outboundFiles + model.pendingDraftFiles }
    private var outboundByteCount: Int64 { retainedOutboundFiles.reduce(0) { $0 + Int64((try? $1.resourceValues(forKeys: [.fileSizeKey]).fileSize) ?? 0) } }
    private func stageDirectory() -> URL {
        if let outboundDirectory { return outboundDirectory }
        let temp = FileManager.default.temporaryDirectory
        // A recreated view can still have pending or restored files from the model.
        let retainedDirectories = Set(retainedOutboundFiles.map { $0.deletingLastPathComponent().deletingLastPathComponent().standardizedFileURL })
        if let old = try? FileManager.default.contentsOfDirectory(at: temp, includingPropertiesForKeys: nil) {
            old.filter { $0.lastPathComponent.hasPrefix("ClipSend-Outbox-") && !retainedDirectories.contains($0.standardizedFileURL) }
                .forEach { try? FileManager.default.removeItem(at: $0) }
        }
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
        if retainedOutboundFiles.isEmpty, let directory = outboundDirectory {
            try? FileManager.default.removeItem(at: directory)
            outboundDirectory = nil
        }
    }
    private func imageType(for mime: String) -> UTType? {
        switch mime.lowercased() { case "image/png": .png; case "image/jpeg", "image/jpg": .jpeg; case "image/heic": .heic; default: nil }
    }
    private func safeImage(_ url: URL, mime: String) -> UIImage? {
        ClipSendImagePreview.load(url, mime: mime, maxPixelSize: 2048)
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
                Button("Pair another computer", systemImage: "qrcode.viewfinder") { pendingPairingMethod = .scan; showSettings = false }
                Button("Paste pairing code") { pendingPairingMethod = .paste; showSettings = false }
                DisclosureGroup("Pair from the command line") {
                    Text("Run clipsend pair iphone on your computer and scan its QR code. Add --code to paste the pairing code instead.")
                        .font(.footnote).foregroundStyle(.secondary).textSelection(.enabled)
                }
            }
            Section("Notifications") {
                if model.authorization == .notDetermined { Button("Enable notifications") { Task { await model.enableNotifications() } } }
                else { Button("Notification settings") { Task { await model.openSettings() } } }
                Text("Tap an alert to receive the item in ClipSend.").font(.footnote).foregroundStyle(.secondary)
            }
            Section("Local network") {
                Button("Check access") { model.checkLocalNetwork() }
                if model.localNetwork == .denied || model.localNetwork == .unavailable {
                    Button("Open Settings") { Task { await model.openAppSettings() } }
                }
                if model.localNetwork == .allowed { Label("Access confirmed", systemImage: "checkmark.circle") }
                if model.localNetwork == .checking { ProgressView("Checking access…") }
            }
            Section("This device") { Button("Refresh push registration") { model.refreshPushRegistration() }; if model.token == nil { Text("Push registration is required for pairing and alerts.").font(.footnote).foregroundStyle(.secondary) } }
            Section { DisclosureGroup("Technical details") { LabeledContent("App identifier", value: Bundle.main.bundleIdentifier ?? "Unavailable"); LabeledContent("Push environment", value: (try? BuildConfiguration.environment().rawValue) ?? "Unavailable") } }
        }.scrollDismissesKeyboard(.interactively).navigationTitle("Settings").navigationBarTitleDisplayMode(.inline).toolbar(.visible, for: .navigationBar).toolbar { ToolbarItem(placement: .topBarTrailing) { Button("Done") { showSettings = false } } } }
    }
    private func pairApprovalSheet(_ invitation: PairingInvitation) -> some View {
        NavigationStack { ScrollView { VStack(alignment: .leading, spacing: 16) {
            Label("Pair with \(invitation.computerName)", systemImage: "desktopcomputer").font(.title2.bold())
            Text("Approve the request on your computer to finish pairing.").foregroundStyle(.secondary)
            TextField("Name this device", text: $deviceName).textFieldStyle(.roundedBorder).textContentType(.name)
            Button { Task { await model.pair(deviceName: deviceName) } } label: { if model.pairing { ProgressView().frame(maxWidth: .infinity) } else { Text("Request pairing").frame(maxWidth: .infinity) } }.buttonStyle(.borderedProminent).controlSize(.large).disabled(model.pairing)
            if model.token == nil { Button("Retry push registration") { model.refreshPushRegistration() }.buttonStyle(.bordered) }
            notificationPrompt
            if let error = model.errorMessage { Text(error).font(.footnote).foregroundStyle(.red) }
            if !model.status.isEmpty { Text(model.status).font(.footnote).foregroundStyle(.secondary) }
            Spacer()
        }.padding(24) }.scrollDismissesKeyboard(.interactively).navigationTitle("Pair computer").navigationBarTitleDisplayMode(.inline).toolbar(.visible, for: .navigationBar).toolbar { ToolbarItem(placement: .topBarTrailing) { Button("Cancel") { model.pairInvitation = nil } } } }
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
    let computer: PairedComputer
    @ObservedObject var model: CompanionModel
    @Environment(\.dismiss) private var dismiss
    @State private var name = ""
    @State private var confirmingForget = false
    @FocusState private var editingName: Bool

    var body: some View {
        Form {
            Section("Computer name") {
                TextField("Name", text: $name).focused($editingName)
                Button("Save name") { if model.rename(computer, to: name) { dismiss() } }
                    .disabled(name.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || name == computer.name)
            }
            Section {
                Button("Check for items") { model.receive(computer.computerID) }
                Button("Forget computer", role: .destructive) { confirmingForget = true }
            }
            if !model.status.isEmpty && !model.status.hasPrefix("No active sends from ") {
                Section { Text(model.status).font(.footnote).foregroundStyle(.secondary) }
            }
            Section {
                DisclosureGroup("Technical details") {
                    LabeledContent("Computer identifier", value: computer.computerID)
                    LabeledContent("Certificate fingerprint", value: computer.certificateSHA256)
                }
            }
        }
        .scrollDismissesKeyboard(.interactively)
        .navigationTitle(computer.name)
        .toolbar { ToolbarItemGroup(placement: .keyboard) { Spacer(); Button("Done") { editingName = false } } }
        .alert("Forget \(computer.name)?", isPresented: $confirmingForget) {
            Button("Forget computer", role: .destructive) { if model.forget(computer) { dismiss() } }
            Button("Cancel", role: .cancel) { confirmingForget = false }
        } message: { Text("You’ll need to pair again to send or receive with this computer.") }
        .onAppear { name = computer.name }
    }
}
