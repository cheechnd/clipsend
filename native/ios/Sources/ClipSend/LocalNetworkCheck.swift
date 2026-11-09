import Foundation
import Network

@MainActor final class LocalNetworkCheck {
    enum State { case idle, checking, allowed, denied, unavailable }
    private var browser: NWBrowser?
    private var listener: NWListener?
    private var timeout: Task<Void, Never>?
    private var generation = UUID()
    private var denied = false

    // Browsing our short-lived Bonjour service confirms that discovery works;
    // merely reaching NWBrowser.ready does not establish permission.
    func start(_ changed: @escaping @MainActor (State) -> Void) {
        stop()
        denied = false
        let generation = self.generation
        let name = "clipsend-setup-" + UUID().uuidString
        changed(.checking)
        do {
            let listener = try NWListener(using: .tcp)
            listener.service = NWListener.Service(name: name, type: "_clipsend._tcp.")
            listener.newConnectionHandler = { $0.cancel() }
            let browser = NWBrowser(for: .bonjour(type: "_clipsend._tcp.", domain: nil), using: .tcp)
            browser.stateUpdateHandler = { [weak self] state in
                Task { @MainActor in
                    guard let self, self.generation == generation else { return }
                    if case .waiting(let error) = state, case .dns(-65570) = error { self.denied = true; changed(.denied) }
                    if case .failed = state { changed(.unavailable); self.stop() }
                }
            }
            browser.browseResultsChangedHandler = { [weak self] results, _ in
                let found = results.contains { result in
                    if case .service(let serviceName, _, _, _) = result.endpoint { return serviceName == name }
                    return false
                }
                guard found else { return }
                Task { @MainActor in
                    guard let self, self.generation == generation else { return }
                    changed(.allowed); self.stop()
                }
            }
            self.listener = listener; self.browser = browser
            listener.start(queue: .main); browser.start(queue: .main)
            timeout = Task { [weak self] in
                try? await Task.sleep(for: .seconds(30))
                guard !Task.isCancelled, let self, self.generation == generation else { return }
                if !self.denied { changed(.unavailable) }; self.stop()
            }
        } catch { changed(.unavailable); stop() }
    }
    func stop() {
        generation = UUID(); timeout?.cancel(); timeout = nil
        browser?.cancel(); listener?.cancel(); browser = nil; listener = nil
    }
}
