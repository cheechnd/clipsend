import SwiftUI
import AVFoundation

// Configure once on the main actor before starting. Blocking camera start/stop
// operations are serialized off the UI thread.
private final class CaptureLifecycle: @unchecked Sendable {
    let session = AVCaptureSession()
    private let queue = DispatchQueue(label: "clipsend.camera")
    func start() { queue.async { self.session.startRunning() } }
    func stop() { queue.async { self.session.stopRunning() } }
}

struct QRScanner: UIViewControllerRepresentable {
    let onCode: (String) -> Void
    func makeUIViewController(context: Context) -> ScannerController { let c = ScannerController(); c.onCode = onCode; return c }
    func updateUIViewController(_ uiViewController: ScannerController, context: Context) { }
}

@MainActor final class ScannerController: UIViewController, @preconcurrency AVCaptureMetadataOutputObjectsDelegate {
    var onCode: ((String) -> Void)?
    private let capture = CaptureLifecycle()
    private var session: AVCaptureSession { capture.session }
    private var configured = false
    private var scanned = false
    override func viewDidLoad() {
        super.viewDidLoad(); view.backgroundColor = .black
        NotificationCenter.default.addObserver(self, selector: #selector(resumeAfterSettings), name: UIApplication.didBecomeActiveNotification, object: nil)
    }
    override func viewDidAppear(_ animated: Bool) {
        super.viewDidAppear(animated)
        Task { await configure() }
    }
    @objc private func resumeAfterSettings() {
        guard view.window != nil, !scanned else { return }
        Task { await configure() }
    }
    private func configure() async {
        switch AVCaptureDevice.authorizationStatus(for: .video) {
        case .authorized: await setup()
        case .notDetermined: if await AVCaptureDevice.requestAccess(for: .video) { await setup() } else { showMessage("Camera access is off. Enable it in Settings, or close the scanner and paste the pairing code.", settings: true) }
        case .denied, .restricted: showMessage("Camera access is off. Enable it in Settings, or close the scanner and paste the pairing code.", settings: true)
        @unknown default: showMessage("Camera access isn’t available. Close the scanner and paste the pairing code.")
        }
    }
    private func setup() async {
        guard view.window != nil, !configured else { return }; configured = true
        guard let camera = AVCaptureDevice.default(for: .video), let input = try? AVCaptureDeviceInput(device: camera), session.canAddInput(input) else { showMessage("No camera is available. Close the scanner and paste the pairing code."); return }
        session.addInput(input)
        let output = AVCaptureMetadataOutput(); guard session.canAddOutput(output) else { showMessage("QR scanning is unavailable. Paste the pairing code instead."); return }
        session.addOutput(output); output.setMetadataObjectsDelegate(self, queue: .main); output.metadataObjectTypes = [.qr]
        let preview = AVCaptureVideoPreviewLayer(session: session); preview.videoGravity = .resizeAspectFill; preview.frame = view.bounds; view.layer.insertSublayer(preview, at: 0)
        let label = UILabel(); label.text = "Point your camera at the ClipSend QR code"; label.textColor = .white; label.textAlignment = .center; label.numberOfLines = 2; label.backgroundColor = UIColor.black.withAlphaComponent(0.55); label.translatesAutoresizingMaskIntoConstraints = false; view.addSubview(label)
        NSLayoutConstraint.activate([label.leadingAnchor.constraint(equalTo: view.leadingAnchor, constant: 24), label.trailingAnchor.constraint(equalTo: view.trailingAnchor, constant: -24), label.bottomAnchor.constraint(equalTo: view.safeAreaLayoutGuide.bottomAnchor, constant: -30), label.heightAnchor.constraint(greaterThanOrEqualToConstant: 54)])
        capture.start()
    }
    private func showMessage(_ text: String, settings: Bool = false) {
        DispatchQueue.main.async { [weak self] in
            guard let self, self.view.window != nil else { return }
            let alert = UIAlertController(title: "QR scanner", message: text, preferredStyle: .alert)
            if settings, let url = URL(string: UIApplication.openSettingsURLString) { alert.addAction(UIAlertAction(title: "Open Settings", style: .default) { _ in UIApplication.shared.open(url) }) }
            alert.addAction(UIAlertAction(title: "Close", style: .cancel))
            self.present(alert, animated: true)
        }
    }
    func metadataOutput(_ output: AVCaptureMetadataOutput, didOutput metadataObjects: [AVMetadataObject], from connection: AVCaptureConnection) {
        guard !scanned, let raw = (metadataObjects.first as? AVMetadataMachineReadableCodeObject)?.stringValue else { return }
        scanned = true
        capture.stop(); onCode?(raw)
    }
    override func viewDidDisappear(_ animated: Bool) { super.viewDidDisappear(animated); capture.stop() }
    override func viewDidLayoutSubviews() { super.viewDidLayoutSubviews(); view.layer.sublayers?.compactMap { $0 as? AVCaptureVideoPreviewLayer }.first?.frame = view.bounds }
}
